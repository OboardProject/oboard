package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/OboardProject/oboard/internal/store"
	"github.com/metacubex/age"
)

const (
	FormatVersion    = 1
	maxArchiveBytes  = int64(4 << 30)
	maxPayloadBytes  = int64(3 << 30)
	maxArchiveFiles  = 10000
	pendingFileName  = "pending-restore.json"
	manifestFileName = "manifest.json"
	payloadFileName  = "payload.age"
)

type SnapshotFunc func(context.Context, string) error

type Config struct {
	Root          string
	DatabasePath  string
	ACMEHome      string
	MasterSecret  string
	SourceVersion string
	Snapshot      SnapshotFunc
}

type Manager struct {
	config Config
}

type Manifest struct {
	FormatVersion int    `json:"format_version"`
	ID            string `json:"id"`
	CreatedAt     string `json:"created_at"`
	SourceVersion string `json:"source_version"`
	PayloadSHA256 string `json:"payload_sha256"`
	PayloadSize   int64  `json:"payload_size"`
}

type Created struct {
	Manifest Manifest
	Path     string
	Size     int64
}

type Inspection struct {
	Manifest Manifest `json:"manifest"`
}

type StagedRestore struct {
	Manifest  Manifest
	StageRoot string
}

type pendingRestore struct {
	ID        string `json:"id"`
	StageRoot string `json:"stage_root"`
	Database  string `json:"database"`
	ACME      string `json:"acme"`
}

func New(config Config) (*Manager, error) {
	config.Root = strings.TrimSpace(config.Root)
	config.DatabasePath = strings.TrimSpace(config.DatabasePath)
	if config.Root == "" || config.DatabasePath == "" || strings.TrimSpace(config.MasterSecret) == "" || config.Snapshot == nil {
		return nil, errors.New("主控备份配置不完整")
	}
	if err := os.MkdirAll(config.Root, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(config.Root, 0o700); err != nil {
		return nil, err
	}
	return &Manager{config: config}, nil
}

func (m *Manager) Root() string { return m.config.Root }

func (m *Manager) Create(ctx context.Context, password string) (Created, error) {
	if strings.TrimSpace(password) == "" {
		return Created{}, errors.New("请设置备份恢复密码")
	}
	id, err := randomID()
	if err != nil {
		return Created{}, err
	}
	work, err := os.MkdirTemp(m.config.Root, ".backup-")
	if err != nil {
		return Created{}, err
	}
	defer os.RemoveAll(work)
	database := filepath.Join(work, "database.sqlite")
	if err := m.config.Snapshot(ctx, database); err != nil {
		return Created{}, fmt.Errorf("创建数据库快照失败：%w", err)
	}
	payload := filepath.Join(work, payloadFileName)
	if err := m.writePayload(payload, database, password); err != nil {
		return Created{}, err
	}
	payloadSHA, payloadSize, err := fileSHA256(payload)
	if err != nil {
		return Created{}, err
	}
	created := time.Now().UTC()
	manifest := Manifest{FormatVersion: FormatVersion, ID: id, CreatedAt: created.Format(time.RFC3339Nano), SourceVersion: m.config.SourceVersion, PayloadSHA256: payloadSHA, PayloadSize: payloadSize}
	name := "oboard-backup-" + created.Format("20060102T150405Z") + "-" + id + ".obk"
	output := filepath.Join(m.config.Root, name)
	if err := writeEnvelope(output, manifest, payload); err != nil {
		return Created{}, err
	}
	info, err := os.Stat(output)
	if err != nil {
		return Created{}, err
	}
	return Created{Manifest: manifest, Path: output, Size: info.Size()}, nil
}

func (m *Manager) SaveUpload(input io.Reader) (string, int64, error) {
	id, err := randomID()
	if err != nil {
		return "", 0, err
	}
	output := filepath.Join(m.config.Root, "oboard-upload-"+id+".obk")
	file, err := os.OpenFile(output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", 0, err
	}
	written, copyErr := io.Copy(file, io.LimitReader(input, maxArchiveBytes+1))
	closeErr := file.Close()
	if copyErr != nil {
		_ = os.Remove(output)
		return "", 0, copyErr
	}
	if closeErr != nil {
		_ = os.Remove(output)
		return "", 0, closeErr
	}
	if written > maxArchiveBytes {
		_ = os.Remove(output)
		return "", 0, fmt.Errorf("备份文件超过 %d GiB 限制", maxArchiveBytes>>30)
	}
	return output, written, nil
}

func (m *Manager) Inspect(archivePath string) (Inspection, error) {
	manifest, _, err := readEnvelope(archivePath, "")
	if err != nil {
		return Inspection{}, err
	}
	return Inspection{Manifest: manifest}, nil
}

func (m *Manager) Verify(archivePath string) (Manifest, error) {
	work, err := os.MkdirTemp(m.config.Root, ".verify-")
	if err != nil {
		return Manifest{}, err
	}
	defer os.RemoveAll(work)
	manifest, _, err := readEnvelope(archivePath, filepath.Join(work, payloadFileName))
	return manifest, err
}

func (m *Manager) Validate(archivePath, password string) (Manifest, error) {
	work, err := os.MkdirTemp(m.config.Root, ".validate-")
	if err != nil {
		return Manifest{}, err
	}
	defer os.RemoveAll(work)
	manifest, _, err := extractArchive(archivePath, password, work)
	return manifest, err
}

func (m *Manager) StageRestore(ctx context.Context, archivePath, password, targetVersion string) (StagedRestore, error) {
	restoreRoot := filepath.Join(m.config.Root, ".restore")
	if err := os.MkdirAll(restoreRoot, 0o700); err != nil {
		return StagedRestore{}, err
	}
	stage, err := os.MkdirTemp(restoreRoot, "stage-")
	if err != nil {
		return StagedRestore{}, err
	}
	manifest, sourceSecret, err := extractArchive(archivePath, password, stage)
	if err != nil {
		_ = os.RemoveAll(stage)
		return StagedRestore{}, err
	}
	if err := CheckCompatibility(manifest.SourceVersion, targetVersion); err != nil {
		_ = os.RemoveAll(stage)
		return StagedRestore{}, err
	}
	database := filepath.Join(stage, "database.sqlite")
	restored, err := store.OpenForRestore(database)
	if err != nil {
		_ = os.RemoveAll(stage)
		return StagedRestore{}, fmt.Errorf("读取备份数据库失败：%w", err)
	}
	if err := restored.CheckIntegrity(ctx); err == nil {
		err = restored.RewrapEncryptedSecrets(ctx, sourceSecret, m.config.MasterSecret)
	}
	if err == nil {
		err = restored.SetSetting(ctx, "controller_backup_restore_reconcile", "true")
	}
	if err == nil {
		err = restored.CheckIntegrity(ctx)
	}
	closeErr := restored.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.RemoveAll(stage)
		return StagedRestore{}, err
	}
	pending := pendingRestore{ID: manifest.ID, StageRoot: stage, Database: database, ACME: filepath.Join(stage, "acme")}
	data, err := json.Marshal(pending)
	if err != nil {
		_ = os.RemoveAll(stage)
		return StagedRestore{}, err
	}
	if err := os.WriteFile(filepath.Join(m.config.Root, pendingFileName), data, 0o600); err != nil {
		_ = os.RemoveAll(stage)
		return StagedRestore{}, err
	}
	return StagedRestore{Manifest: manifest, StageRoot: stage}, nil
}

func ApplyPendingRestore(config Config) error {
	data, err := os.ReadFile(filepath.Join(config.Root, pendingFileName))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var pending pendingRestore
	if err := json.Unmarshal(data, &pending); err != nil {
		return fmt.Errorf("读取待恢复状态失败：%w", err)
	}
	restoreRoot := filepath.Join(config.Root, ".restore")
	if !validBackupID(pending.ID) || !within(restoreRoot, pending.StageRoot) || filepath.Clean(pending.Database) != filepath.Join(filepath.Clean(pending.StageRoot), "database.sqlite") || filepath.Clean(pending.ACME) != filepath.Join(filepath.Clean(pending.StageRoot), "acme") {
		return errors.New("待恢复状态包含无效路径")
	}
	databaseExisted := pathExists(config.DatabasePath)
	databaseRollback := config.DatabasePath + ".restore-rollback-" + pending.ID
	if err := replaceFile(pending.Database, config.DatabasePath, pending.ID); err != nil {
		return err
	}
	if err := replaceTree(pending.ACME, config.ACMEHome, pending.ID); err != nil {
		if rollbackErr := rollbackPath(config.DatabasePath, databaseRollback, databaseExisted); rollbackErr != nil {
			return errors.Join(err, fmt.Errorf("恢复数据库回滚失败：%w", rollbackErr))
		}
		return err
	}
	if err := os.Remove(filepath.Join(config.Root, pendingFileName)); err != nil {
		return err
	}
	_ = os.Remove(databaseRollback)
	_ = os.RemoveAll(config.ACMEHome + ".restore-rollback-" + pending.ID)
	return os.RemoveAll(pending.StageRoot)
}

func (m *Manager) writePayload(destination, database, password string) error {
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	recipient, err := age.NewScryptRecipient(password)
	if err != nil {
		_ = output.Close()
		return err
	}
	ageWriter, err := age.Encrypt(output, recipient)
	if err != nil {
		_ = output.Close()
		return err
	}
	gzipWriter := gzip.NewWriter(ageWriter)
	tarWriter := tar.NewWriter(gzipWriter)
	err = addPayloadFile(tarWriter, database, "database.sqlite")
	if err == nil {
		secret, marshalErr := json.Marshal(map[string]string{"master_secret": m.config.MasterSecret})
		if marshalErr != nil {
			err = marshalErr
		} else {
			err = addPayloadBytes(tarWriter, "secrets.json", secret)
		}
	}
	if err == nil && strings.TrimSpace(m.config.ACMEHome) != "" {
		err = addPayloadTree(tarWriter, m.config.ACMEHome, "acme")
	}
	closeTar := tarWriter.Close()
	closeGzip := gzipWriter.Close()
	closeAge := ageWriter.Close()
	closeFile := output.Close()
	if err != nil {
		_ = os.Remove(destination)
		return err
	}
	for _, closeErr := range []error{closeTar, closeGzip, closeAge, closeFile} {
		if closeErr != nil {
			_ = os.Remove(destination)
			return closeErr
		}
	}
	return nil
}

func writeEnvelope(destination string, manifest Manifest, payload string) error {
	temp, err := os.CreateTemp(filepath.Dir(destination), ".backup-envelope-")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	w := tar.NewWriter(temp)
	data, err := json.Marshal(manifest)
	if err == nil {
		err = addPayloadBytes(w, manifestFileName, data)
	}
	if err == nil {
		err = addPayloadFile(w, payload, payloadFileName)
	}
	closeTar := w.Close()
	closeFile := temp.Close()
	if err != nil {
		return err
	}
	if closeTar != nil {
		return closeTar
	}
	if closeFile != nil {
		return closeFile
	}
	return os.Rename(tempName, destination)
}

func readEnvelope(archivePath, payloadDestination string) (Manifest, int64, error) {
	file, err := os.Open(archivePath)
	if err != nil {
		return Manifest{}, 0, err
	}
	defer file.Close()
	reader := tar.NewReader(io.LimitReader(file, maxArchiveBytes+1))
	var manifest Manifest
	var gotManifest, gotPayload bool
	var payloadSize int64
	var payloadWriter *os.File
	if payloadDestination != "" {
		payloadWriter, err = os.OpenFile(payloadDestination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return Manifest{}, 0, err
		}
		defer payloadWriter.Close()
	}
	for entries := 0; ; entries++ {
		if entries > 2 {
			return Manifest{}, 0, errors.New("备份文件包含多余内容")
		}
		header, nextErr := reader.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			return Manifest{}, 0, nextErr
		}
		if header.Typeflag != tar.TypeReg || header.Size < 0 || header.Size > maxPayloadBytes {
			return Manifest{}, 0, errors.New("备份文件包含不安全内容")
		}
		switch header.Name {
		case manifestFileName:
			if gotManifest || header.Size > 1<<20 {
				return Manifest{}, 0, errors.New("备份清单无效")
			}
			data, readErr := io.ReadAll(io.LimitReader(reader, header.Size+1))
			if readErr != nil || int64(len(data)) != header.Size {
				return Manifest{}, 0, errors.New("备份清单不完整")
			}
			if err := json.Unmarshal(data, &manifest); err != nil {
				return Manifest{}, 0, errors.New("备份清单无效")
			}
			gotManifest = true
		case payloadFileName:
			if gotPayload {
				return Manifest{}, 0, errors.New("备份数据重复")
			}
			payloadSize = header.Size
			if payloadWriter != nil {
				written, copyErr := io.Copy(payloadWriter, io.LimitReader(reader, header.Size+1))
				if copyErr != nil || written != header.Size {
					return Manifest{}, 0, errors.New("备份数据不完整")
				}
			} else if _, err := io.Copy(io.Discard, io.LimitReader(reader, header.Size+1)); err != nil {
				return Manifest{}, 0, err
			}
			gotPayload = true
		default:
			return Manifest{}, 0, errors.New("备份文件包含未知内容")
		}
	}
	if !gotManifest || !gotPayload || manifest.FormatVersion != FormatVersion || !validBackupID(manifest.ID) || len(manifest.PayloadSHA256) != sha256.Size*2 || manifest.PayloadSize != payloadSize {
		return Manifest{}, 0, errors.New("备份文件不完整或格式不受支持")
	}
	if payloadDestination != "" {
		if err := payloadWriter.Close(); err != nil {
			return Manifest{}, 0, err
		}
		payloadWriter = nil
		sha, _, err := fileSHA256(payloadDestination)
		if err != nil || !strings.EqualFold(sha, manifest.PayloadSHA256) {
			return Manifest{}, 0, errors.New("备份数据完整性校验失败")
		}
	}
	return manifest, payloadSize, nil
}

func extractArchive(archivePath, password, stage string) (Manifest, string, error) {
	payload := filepath.Join(stage, payloadFileName)
	manifest, _, err := readEnvelope(archivePath, payload)
	if err != nil {
		return Manifest{}, "", err
	}
	input, err := os.Open(payload)
	if err != nil {
		return Manifest{}, "", err
	}
	defer input.Close()
	identity, err := age.NewScryptIdentity(password)
	if err != nil {
		return Manifest{}, "", err
	}
	decrypted, err := age.Decrypt(input, identity)
	if err != nil {
		return Manifest{}, "", errors.New("恢复密码错误或备份文件已损坏")
	}
	gzipReader, err := gzip.NewReader(io.LimitReader(decrypted, maxPayloadBytes+1))
	if err != nil {
		return Manifest{}, "", errors.New("备份数据无效")
	}
	defer gzipReader.Close()
	reader := tar.NewReader(gzipReader)
	var gotDatabase, gotSecrets bool
	var secretData []byte
	var total int64
	for entries := 0; ; entries++ {
		if entries > maxArchiveFiles {
			return Manifest{}, "", errors.New("备份包含的文件过多")
		}
		header, nextErr := reader.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			return Manifest{}, "", nextErr
		}
		name := path.Clean(strings.TrimPrefix(header.Name, "./"))
		if name == "." || strings.HasPrefix(name, "../") || path.IsAbs(name) || header.Size < 0 || header.Size > maxPayloadBytes {
			return Manifest{}, "", errors.New("备份包含不安全路径")
		}
		if header.Typeflag == tar.TypeDir {
			if name != "acme" && !strings.HasPrefix(name, "acme/") {
				return Manifest{}, "", errors.New("备份包含未知目录")
			}
			if err := os.MkdirAll(filepath.Join(stage, filepath.FromSlash(name)), 0o700); err != nil {
				return Manifest{}, "", err
			}
			continue
		}
		if header.Typeflag != tar.TypeReg {
			return Manifest{}, "", errors.New("备份包含不安全文件")
		}
		total += header.Size
		if total > maxPayloadBytes {
			return Manifest{}, "", errors.New("备份解压后超过允许大小")
		}
		switch {
		case name == "database.sqlite":
			if gotDatabase {
				return Manifest{}, "", errors.New("备份数据库重复")
			}
			if err := writeTarFile(reader, filepath.Join(stage, "database.sqlite"), header.Size); err != nil {
				return Manifest{}, "", err
			}
			gotDatabase = true
		case name == "secrets.json":
			if gotSecrets || header.Size > 1<<20 {
				return Manifest{}, "", errors.New("备份恢复信息无效")
			}
			secretData, err = io.ReadAll(io.LimitReader(reader, header.Size+1))
			if err != nil || int64(len(secretData)) != header.Size {
				return Manifest{}, "", errors.New("备份恢复信息不完整")
			}
			gotSecrets = true
		case strings.HasPrefix(name, "acme/"):
			if err := writeTarFile(reader, filepath.Join(stage, filepath.FromSlash(name)), header.Size); err != nil {
				return Manifest{}, "", err
			}
		default:
			return Manifest{}, "", errors.New("备份包含未知文件")
		}
	}
	if !gotDatabase || !gotSecrets {
		return Manifest{}, "", errors.New("备份内容不完整")
	}
	var secrets map[string]string
	if err := json.Unmarshal(secretData, &secrets); err != nil || strings.TrimSpace(secrets["master_secret"]) == "" {
		return Manifest{}, "", errors.New("备份恢复信息无效")
	}
	return manifest, secrets["master_secret"], nil
}

func addPayloadBytes(writer *tar.Writer, name string, value []byte) error {
	if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(value)), Typeflag: tar.TypeReg, ModTime: time.Now().UTC()}); err != nil {
		return err
	}
	_, err := writer.Write(value)
	return err
}

func addPayloadFile(writer *tar.Writer, source, name string) error {
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > maxPayloadBytes {
		return errors.New("备份源文件无效")
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: info.Size(), Typeflag: tar.TypeReg, ModTime: time.Now().UTC()}); err != nil {
		return err
	}
	written, err := io.Copy(writer, input)
	if err != nil {
		return err
	}
	if written != info.Size() {
		return errors.New("备份源文件读取不完整")
	}
	return nil
}

func addPayloadTree(writer *tar.Writer, source, prefix string) error {
	info, err := os.Lstat(source)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("证书续期状态目录无效")
	}
	entries := 0
	return filepath.WalkDir(source, func(item string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if item == source {
			return nil
		}
		entries++
		if entries > maxArchiveFiles {
			return errors.New("证书续期状态文件过多")
		}
		relative, err := filepath.Rel(source, item)
		if err != nil {
			return err
		}
		name := path.Join(prefix, filepath.ToSlash(relative))
		itemInfo, err := entry.Info()
		if err != nil {
			return err
		}
		if itemInfo.Mode()&os.ModeSymlink != 0 {
			return errors.New("证书续期状态不能包含符号链接")
		}
		if itemInfo.IsDir() {
			return writer.WriteHeader(&tar.Header{Name: name, Mode: 0o700, Typeflag: tar.TypeDir, ModTime: time.Now().UTC()})
		}
		if !itemInfo.Mode().IsRegular() {
			return errors.New("证书续期状态包含不支持的文件")
		}
		return addPayloadFile(writer, item, name)
	})
}

func writeTarFile(reader *tar.Reader, destination string, size int64) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	written, copyErr := io.Copy(output, io.LimitReader(reader, size+1))
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if written != size {
		return errors.New("备份文件读取不完整")
	}
	return nil
}

func fileSHA256(filePath string) (string, int64, error) {
	input, err := os.Open(filePath)
	if err != nil {
		return "", 0, err
	}
	defer input.Close()
	hash := sha256.New()
	count, err := io.Copy(hash, input)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), count, nil
}

func randomID() (string, error) {
	value := make([]byte, 12)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func validBackupID(value string) bool {
	if len(value) != 24 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 12 && strings.ToLower(value) == value
}

func compatibleVersion(source, target string) bool {
	source = strings.TrimSpace(strings.TrimPrefix(source, "v"))
	target = strings.TrimSpace(strings.TrimPrefix(target, "v"))
	if source == target || strings.Contains(target, "dev") {
		return true
	}
	if strings.Contains(source, "dev") {
		return false
	}
	parse := func(value string) ([3]int, string, bool) {
		var result [3]int
		value = strings.SplitN(value, "+", 2)[0]
		versionParts := strings.SplitN(value, "-", 2)
		parts := strings.Split(versionParts[0], ".")
		if len(parts) != 3 {
			return result, "", false
		}
		for i := range parts {
			v, err := strconv.Atoi(parts[i])
			if err != nil || v < 0 {
				return result, "", false
			}
			result[i] = v
		}
		prerelease := ""
		if len(versionParts) == 2 {
			prerelease = versionParts[1]
		}
		return result, prerelease, true
	}
	sourceVersion, sourcePrerelease, sourceOK := parse(source)
	targetVersion, targetPrerelease, targetOK := parse(target)
	if !sourceOK || !targetOK {
		return false
	}
	for i := range sourceVersion {
		if targetVersion[i] != sourceVersion[i] {
			return targetVersion[i] > sourceVersion[i]
		}
	}
	if sourcePrerelease == targetPrerelease {
		return true
	}
	if sourcePrerelease == "" {
		return false
	}
	if targetPrerelease == "" {
		return true
	}
	return comparePrerelease(targetPrerelease, sourcePrerelease) >= 0
}

func comparePrerelease(left, right string) int {
	leftParts := strings.Split(left, ".")
	rightParts := strings.Split(right, ".")
	for i := 0; i < len(leftParts) && i < len(rightParts); i++ {
		if leftParts[i] == rightParts[i] {
			continue
		}
		leftNumber, leftErr := strconv.Atoi(leftParts[i])
		rightNumber, rightErr := strconv.Atoi(rightParts[i])
		switch {
		case leftErr == nil && rightErr == nil:
			if leftNumber < rightNumber {
				return -1
			}
			return 1
		case leftErr == nil:
			return -1
		case rightErr == nil:
			return 1
		case leftParts[i] < rightParts[i]:
			return -1
		default:
			return 1
		}
	}
	if len(leftParts) < len(rightParts) {
		return -1
	}
	if len(leftParts) > len(rightParts) {
		return 1
	}
	return 0
}

func CheckCompatibility(source, target string) error {
	if compatibleVersion(source, target) {
		return nil
	}
	return fmt.Errorf("来源版本 %s 不能恢复到主控版本 %s，请先升级目标主控", source, target)
}

func within(root, candidate string) bool {
	root, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	candidate, err = filepath.Abs(candidate)
	if err != nil {
		return false
	}
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator))
}

func replaceFile(source, destination, id string) error {
	if !within(filepath.Dir(source), source) {
		return errors.New("恢复数据库路径无效")
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	pending := destination + ".restore-new-" + id
	rollback := destination + ".restore-rollback-" + id
	_ = os.Remove(pending)
	if err := copyRegularFile(source, pending, 0o600); err != nil {
		return err
	}
	if _, err := os.Stat(destination); err == nil {
		_ = os.Remove(rollback)
		if err := os.Rename(destination, rollback); err != nil {
			_ = os.Remove(pending)
			return err
		}
	}
	if err := os.Rename(pending, destination); err != nil {
		_ = os.Remove(pending)
		if rollbackErr := rollbackPath(destination, rollback, pathExists(rollback)); rollbackErr != nil {
			return errors.Join(err, rollbackErr)
		}
		return err
	}
	return nil
}

func replaceTree(source, destination, id string) error {
	if _, err := os.Stat(source); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	pending := destination + ".restore-new-" + id
	rollback := destination + ".restore-rollback-" + id
	_ = os.RemoveAll(pending)
	if err := copyTree(source, pending); err != nil {
		return err
	}
	if _, err := os.Stat(destination); err == nil {
		_ = os.RemoveAll(rollback)
		if err := os.Rename(destination, rollback); err != nil {
			_ = os.RemoveAll(pending)
			return err
		}
	}
	if err := os.Rename(pending, destination); err != nil {
		_ = os.RemoveAll(pending)
		if rollbackErr := rollbackPath(destination, rollback, pathExists(rollback)); rollbackErr != nil {
			return errors.Join(err, rollbackErr)
		}
		return err
	}
	return nil
}

func rollbackPath(destination, rollback string, existed bool) error {
	if err := os.RemoveAll(destination); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if !existed {
		return nil
	}
	return os.Rename(rollback, destination)
}

func pathExists(value string) bool {
	_, err := os.Stat(value)
	return err == nil
}

func copyTree(source, destination string) error {
	return filepath.WalkDir(source, func(item string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, item)
		if err != nil {
			return err
		}
		target := destination
		if relative != "." {
			target = filepath.Join(destination, relative)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("恢复数据不能包含符号链接")
		}
		if info.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if !info.Mode().IsRegular() {
			return errors.New("恢复数据包含不支持的文件")
		}
		return copyRegularFile(item, target, 0o600)
	})
}

func copyRegularFile(source, destination string, mode os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}
