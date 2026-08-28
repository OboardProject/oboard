package controller

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"net"
	"sort"
	"strings"
	"time"
)

const (
	controllerNTPPacketSize    = 48
	controllerNTPEpochDelta    = 2208988800
	controllerNTPRefreshPeriod = time.Minute
	controllerNTPMaxAge        = 2 * time.Minute
	controllerNTPBatchTimeout  = 5 * time.Second
)

type controllerNTPQueryFunc func(context.Context, string) (time.Duration, error)

type controllerNTPReference struct {
	reference   time.Time
	localAnchor time.Time
	source      string
}

type controllerNTPSample struct {
	offset time.Duration
	source string
	err    error
}

type controllerNTPState struct {
	reference   time.Time
	localAnchor time.Time
	source      string
	lastError   string
}

func (s *Server) refreshControllerTime(ctx context.Context) {
	settings, err := s.store.ListSettings(ctx)
	if err != nil {
		s.recordControllerNTPError(err)
		return
	}
	query := s.controllerNTPQuery
	if query == nil {
		query = queryControllerNTP
	}
	reference, err := queryControllerNTPMedian(ctx, timeCheckNTPServers(settings), query)
	if err != nil {
		s.recordControllerNTPError(err)
		return
	}
	s.controllerNTPMu.Lock()
	s.controllerNTPState = controllerNTPState{
		reference:   reference.reference,
		localAnchor: reference.localAnchor,
		source:      reference.source,
	}
	s.controllerNTPMu.Unlock()
}

func (s *Server) recordControllerNTPError(err error) {
	message := strings.TrimSpace(err.Error())
	s.controllerNTPMu.Lock()
	s.controllerNTPState.lastError = message
	s.controllerNTPMu.Unlock()
	s.logPeriodicError("controller-ntp", "Controller NTP refresh failed: %v", err)
}

func (s *Server) controllerTimeNow() (time.Time, string, bool) {
	if s == nil {
		return time.Time{}, "", false
	}
	s.controllerNTPMu.RLock()
	state := s.controllerNTPState
	s.controllerNTPMu.RUnlock()
	if state.reference.IsZero() || state.localAnchor.IsZero() {
		return time.Time{}, "", false
	}
	age := time.Since(state.localAnchor)
	if age < 0 || age > controllerNTPMaxAge {
		return time.Time{}, "", false
	}
	return state.reference.Add(age).UTC(), state.source, true
}

func (s *Server) withControllerTime(payload any) any {
	message, ok := payload.(map[string]any)
	if !ok {
		return payload
	}
	copyMessage := make(map[string]any, len(message)+1)
	for key, value := range message {
		copyMessage[key] = value
	}
	controllerTime, source, available := s.controllerTimeNow()
	if !available {
		delete(copyMessage, "ts")
		delete(copyMessage, "ts_source")
		return copyMessage
	}
	copyMessage["ts"] = controllerTime
	copyMessage["ts_source"] = source
	return copyMessage
}

func queryControllerNTPMedian(ctx context.Context, servers []string, query controllerNTPQueryFunc) (controllerNTPReference, error) {
	queryCtx, cancel := context.WithTimeout(ctx, controllerNTPBatchTimeout)
	defer cancel()
	results := make(chan controllerNTPSample, len(servers))
	for _, server := range servers {
		server := server
		go func() {
			offset, err := query(queryCtx, server)
			results <- controllerNTPSample{offset: offset, source: server, err: err}
		}()
	}
	samples := make([]controllerNTPSample, 0, len(servers))
	errorsByServer := make([]string, 0, len(servers))
	for range servers {
		select {
		case sample := <-results:
			if sample.err != nil {
				errorsByServer = append(errorsByServer, sample.source+": "+sample.err.Error())
			} else {
				samples = append(samples, sample)
			}
		case <-queryCtx.Done():
			errorsByServer = append(errorsByServer, queryCtx.Err().Error())
		}
	}
	if len(samples) < 2 {
		return controllerNTPReference{}, fmt.Errorf("Controller 至少需要两个 NTP 源返回结果: %s", strings.Join(errorsByServer, "; "))
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i].offset < samples[j].offset })
	offset := samples[len(samples)/2].offset
	if len(samples)%2 == 0 {
		offset = samples[len(samples)/2-1].offset/2 + samples[len(samples)/2].offset/2
	}
	local := time.Now()
	sources := make([]string, 0, len(samples))
	for _, sample := range samples {
		sources = append(sources, sample.source)
	}
	return controllerNTPReference{
		reference:   local.Add(offset),
		localAnchor: local,
		source:      "ntp:" + strings.Join(sources, ","),
	}, nil
}

func queryControllerNTP(ctx context.Context, server string) (time.Duration, error) {
	dialer := net.Dialer{Timeout: 4 * time.Second}
	conn, err := dialer.DialContext(ctx, "udp", net.JoinHostPort(server, "123"))
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	request := make([]byte, controllerNTPPacketSize)
	request[0] = 0x23
	t1 := time.Now()
	transmit, err := encodeControllerNTPTimestamp(t1)
	if err != nil {
		return 0, err
	}
	binary.BigEndian.PutUint64(request[40:48], transmit)
	if _, err := conn.Write(request); err != nil {
		return 0, err
	}
	response := make([]byte, controllerNTPPacketSize)
	if _, err := conn.Read(response); err != nil {
		return 0, err
	}
	t4 := time.Now()
	if response[0]&0x7 != 4 || response[1] == 0 || response[1] > 15 {
		return 0, errors.New("invalid NTP server response")
	}
	if binary.BigEndian.Uint64(response[24:32]) != transmit {
		return 0, errors.New("NTP originate timestamp mismatch")
	}
	t2 := decodeControllerNTPTimestamp(binary.BigEndian.Uint64(response[32:40]))
	t3 := decodeControllerNTPTimestamp(binary.BigEndian.Uint64(response[40:48]))
	if t2.IsZero() || t3.IsZero() {
		return 0, errors.New("NTP response timestamp is missing")
	}
	return (t2.Sub(t1) + t3.Sub(t4)) / 2, nil
}

func encodeControllerNTPTimestamp(value time.Time) (uint64, error) {
	seconds := value.Unix() + controllerNTPEpochDelta
	if seconds < 0 || seconds > int64(math.MaxUint32) {
		return 0, errors.New("local time is outside the supported NTP era")
	}
	fraction := (uint64(value.Nanosecond()) << 32) / 1_000_000_000
	return uint64(seconds)<<32 | fraction, nil
}

func decodeControllerNTPTimestamp(value uint64) time.Time {
	seconds := int64(uint32(value>>32)) - controllerNTPEpochDelta
	fraction := value & math.MaxUint32
	nanoseconds := int64(fraction * 1_000_000_000 >> 32)
	return time.Unix(seconds, nanoseconds).UTC()
}
