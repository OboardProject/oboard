# OBoard Agent installer for Windows. Served by the Controller at
# /install/agent.ps1; install, update, and uninstall are selected with
# OBOARD_ACTION. Requires Windows PowerShell 5.1 or later and an elevated
# session.
#
# The whole installation lives below one root (%ProgramData%\oboard-agent):
# bin, config, state, logs, and the kernel socket directory. The root is
# restricted to SYSTEM and Administrators because the Agent and the kernel run
# as LocalSystem and the configuration carries the Agent token.

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

function Invoke-OBoardAgentInstaller {
    $DefaultBaseUrl = __BASE_URL__
    $ReleasePublicKey = __RELEASE_PUBLIC_KEY__
    [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

    $Action = if ($env:OBOARD_ACTION) { $env:OBOARD_ACTION.Trim().ToLowerInvariant() } else { 'install' }
    $BaseUrl = if ($env:OBOARD_CONTROLLER_URL) { $env:OBOARD_CONTROLLER_URL.Trim() } else { $DefaultBaseUrl }
    $BaseUrl = $BaseUrl.TrimEnd('/')
    $EnrollToken = $env:OBOARD_ENROLL_TOKEN
    # The command runs in the operator's own session; clear what it set so a
    # later command in the same window does not inherit a stale action or
    # token.
    Remove-Item Env:\OBOARD_ENROLL_TOKEN -ErrorAction SilentlyContinue
    Remove-Item Env:\OBOARD_ACTION -ErrorAction SilentlyContinue
    $Purge = $env:OBOARD_PURGE -ne '0'
    $AllowPanelUpdate = $env:OBOARD_ALLOW_PANEL_UPDATE
    $UpdateSource = $env:OBOARD_UPDATE_SOURCE
    $UpdateRepo = if ($env:OBOARD_UPDATE_REPO) { $env:OBOARD_UPDATE_REPO } else { 'OboardProject/oboard-agent' }

    if ($Action -notin @('install', 'update', 'uninstall')) {
        throw "不支持的操作：$Action（可用：install、update、uninstall）"
    }
    $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
    $principal = New-Object Security.Principal.WindowsPrincipal($identity)
    if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
        throw '请在“以管理员身份运行”的 PowerShell 中执行此命令。'
    }
    if ($env:OBOARD_INSTALL_STEALTH -eq '1') {
        throw 'Windows Agent 暂不支持安全进程布局，请在面板关闭该服务器的安全进程后重新复制安装命令。'
    }
    if ($UpdateRepo -notmatch '^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$') {
        throw '更新仓库格式无效，请使用 owner/name 格式。'
    }

    $arch = $env:PROCESSOR_ARCHITECTURE
    if ($env:PROCESSOR_ARCHITEW6432) { $arch = $env:PROCESSOR_ARCHITEW6432 }
    if ($arch -ne 'AMD64') {
        throw "当前系统暂不支持：windows/$arch（目前支持 64 位 x86 Windows）"
    }
    if (-not [Environment]::Is64BitProcess) {
        throw '请使用 64 位 PowerShell 执行此命令。'
    }
    $OsValue = 'windows'
    $ArchValue = 'amd64'

    $ProgramData = if ($env:ProgramData) { $env:ProgramData } else { 'C:\ProgramData' }
    $Root = Join-Path $ProgramData 'oboard-agent'
    # The Agent rejects managed paths containing spaces, quotes, or shell
    # metacharacters; fail before anything is written.
    if ($Root -match '[\s''"$;&|<>()*?\[\]{}!#~%]') {
        throw "安装目录包含不支持的字符：$Root"
    }
    $BinDir = Join-Path $Root 'bin'
    $ConfigDir = Join-Path $Root 'config'
    $ConfigPath = Join-Path $ConfigDir 'config.json'
    $StateDir = Join-Path $Root 'state'
    $LogDir = Join-Path $Root 'logs'
    $RunDir = Join-Path $Root 'run'
    $CoreSocket = Join-Path $RunDir 'oboard-sb.sock'
    $AgentExe = Join-Path $BinDir 'oboard-agent.exe'
    $CoreExe = Join-Path $BinDir 'oboard-sb.exe'
    $RealmExe = Join-Path $BinDir 'oboard-realm.exe'
    $AgentService = 'oboard-agent'
    $CoreService = 'oboard-sb'
    $script:InstallLog = Join-Path $LogDir 'install.log'
    $script:LockStream = $null

    function Write-Step([string]$Message) { Write-Host $Message }

    function Write-InstallLog([string[]]$Lines) {
        if (-not $Lines) { return }
        try { Add-Content -LiteralPath $script:InstallLog -Value $Lines -Encoding UTF8 } catch { }
    }

    function Invoke-Native([string]$File, [string[]]$Arguments) {
        $previous = $ErrorActionPreference
        $ErrorActionPreference = 'Continue'
        try {
            $output = & $File @Arguments 2>&1 | ForEach-Object { "$_" }
            $code = $LASTEXITCODE
        } finally {
            $ErrorActionPreference = $previous
        }
        Write-InstallLog $output
        return $code
    }

    function Invoke-NativeOutput([string]$File, [string[]]$Arguments) {
        $previous = $ErrorActionPreference
        $ErrorActionPreference = 'Continue'
        try {
            return (& $File @Arguments 2>&1 | ForEach-Object { "$_" }) -join [Environment]::NewLine
        } finally {
            $ErrorActionPreference = $previous
        }
    }

    function Protect-Root {
        New-Item -ItemType Directory -Force -Path $Root | Out-Null
        $icacls = Join-Path $env:SystemRoot 'System32\icacls.exe'
        # SYSTEM (S-1-5-18) and BUILTIN\Administrators (S-1-5-32-544) only.
        if ((Invoke-Native $icacls @($Root, '/inheritance:r', '/grant:r', '*S-1-5-18:(OI)(CI)F', '*S-1-5-32-544:(OI)(CI)F')) -ne 0) {
            throw "无法设置安装目录权限：$Root"
        }
        foreach ($dir in @($BinDir, $ConfigDir, $StateDir, $LogDir, $RunDir)) {
            New-Item -ItemType Directory -Force -Path $dir | Out-Null
        }
        # Children created before the root was restricted keep their old
        # explicit entries; reset them so they inherit from the root.
        [void](Invoke-Native $icacls @((Join-Path $Root '*'), '/reset', '/T', '/C', '/Q'))
    }

    # The Agent serializes kernel lifecycle work with a byte-range lock on the
    # same file (LockFileEx), so an update never races a panel deployment.
    function Enter-CoreLifecycleLock {
        $lockPath = Join-Path $StateDir 'core-lifecycle.lock'
        try {
            $stream = [IO.File]::Open($lockPath, [IO.FileMode]::OpenOrCreate, [IO.FileAccess]::ReadWrite, [IO.FileShare]::ReadWrite)
        } catch {
            Write-Host "无法创建更新互斥锁 $lockPath，继续执行。"
            return
        }
        for ($waited = 0; ; $waited++) {
            try {
                $stream.Lock(0, 1)
                break
            } catch [IO.IOException] {
                if ($waited -ge 120) {
                    $stream.Dispose()
                    throw '另一个 OBoard 更新或面板配置下发正在进行，请稍后重试。'
                }
                Start-Sleep -Seconds 1
            }
        }
        $holder = [Text.Encoding]::ASCII.GetBytes("installer $PID" + [char]10)
        $stream.SetLength(0)
        $stream.Write($holder, 0, $holder.Length)
        $stream.Flush()
        $script:LockStream = $stream
    }

    function Exit-CoreLifecycleLock {
        if ($script:LockStream) {
            try { $script:LockStream.Unlock(0, 1) } catch { }
            $script:LockStream.Dispose()
            $script:LockStream = $null
        }
    }

    function Invoke-Download([string]$Label, [string]$Url, [string]$Destination) {
        Write-Step "  $Label"
        foreach ($candidate in @($Url, "$Url" + '?source=controller')) {
            for ($attempt = 1; $attempt -le 3; $attempt++) {
                try {
                    Invoke-WebRequest -UseBasicParsing -Uri $candidate -OutFile $Destination -TimeoutSec 300
                    return
                } catch {
                    Write-InstallLog "download $candidate attempt $attempt failed: $($_.Exception.Message)"
                    Start-Sleep -Seconds $attempt
                }
            }
            Write-Host '  下载未完成，尝试直接从主控下载...'
        }
        throw "下载失败：$Label"
    }

    function Initialize-ReleaseVerifier {
        if ('OBoard.ReleaseVerifier' -as [type]) { return }
        Add-Type -ReferencedAssemblies System.Numerics -TypeDefinition @'
using System;
using System.Numerics;
using System.Security.Cryptography;

namespace OBoard {
    // Ed25519 signature verification (RFC 8032, section 5.1.7). Windows
    // PowerShell has no Ed25519 primitive, and a fresh host has no trusted
    // OBoard binary to delegate to, so the installer verifies the signed
    // release manifest itself.
    public static class ReleaseVerifier {
        static readonly BigInteger P = BigInteger.Pow(2, 255) - 19;
        static readonly BigInteger L = BigInteger.Pow(2, 252) + BigInteger.Parse("27742317777372353535851937790883648493");
        static readonly BigInteger D = Mod(-121665 * Inv(121666));
        static readonly BigInteger SqrtM1 = BigInteger.ModPow(2, (P - 1) / 4, P);
        static readonly BigInteger[] G = BasePoint();

        static BigInteger Mod(BigInteger value) {
            BigInteger r = value % P;
            return r.Sign < 0 ? r + P : r;
        }

        static BigInteger Inv(BigInteger value) {
            return BigInteger.ModPow(value, P - 2, P);
        }

        static BigInteger FromLittleEndian(byte[] bytes, int offset, int count) {
            byte[] buffer = new byte[count + 1];
            Array.Copy(bytes, offset, buffer, 0, count);
            return new BigInteger(buffer);
        }

        static BigInteger[] Add(BigInteger[] p, BigInteger[] q) {
            BigInteger a = Mod((p[1] - p[0]) * (q[1] - q[0]));
            BigInteger b = Mod((p[1] + p[0]) * (q[1] + q[0]));
            BigInteger c = Mod(2 * p[3] * q[3] * D);
            BigInteger d = Mod(2 * p[2] * q[2]);
            BigInteger e = b - a, f = d - c, g = d + c, h = b + a;
            return new BigInteger[] { Mod(e * f), Mod(g * h), Mod(f * g), Mod(e * h) };
        }

        static BigInteger[] Multiply(BigInteger scalar, BigInteger[] point) {
            BigInteger[] result = new BigInteger[] { 0, 1, 1, 0 };
            while (scalar.Sign > 0) {
                if (!scalar.IsEven) { result = Add(result, point); }
                point = Add(point, point);
                scalar >>= 1;
            }
            return result;
        }

        static bool Equal(BigInteger[] p, BigInteger[] q) {
            return Mod(p[0] * q[2] - q[0] * p[2]).IsZero && Mod(p[1] * q[2] - q[1] * p[2]).IsZero;
        }

        static BigInteger? RecoverX(BigInteger y, int sign) {
            if (y >= P) { return null; }
            BigInteger x2 = Mod((y * y - 1) * Inv(Mod(D * y * y + 1)));
            if (x2.IsZero) {
                if (sign != 0) { return null; }
                return BigInteger.Zero;
            }
            BigInteger x = BigInteger.ModPow(x2, (P + 3) / 8, P);
            if (!Mod(x * x - x2).IsZero) { x = Mod(x * SqrtM1); }
            if (!Mod(x * x - x2).IsZero) { return null; }
            if ((int)(x & 1) != sign) { x = P - x; }
            return x;
        }

        static BigInteger[] Decompress(byte[] bytes, int offset) {
            byte[] copy = new byte[32];
            Array.Copy(bytes, offset, copy, 0, 32);
            int sign = copy[31] >> 7;
            copy[31] &= 0x7f;
            BigInteger y = FromLittleEndian(copy, 0, 32);
            BigInteger? x = RecoverX(y, sign);
            if (x == null) { return null; }
            return new BigInteger[] { x.Value, y, 1, Mod(x.Value * y) };
        }

        static BigInteger[] BasePoint() {
            BigInteger y = Mod(4 * Inv(5));
            BigInteger x = RecoverX(y, 0).Value;
            return new BigInteger[] { x, y, 1, Mod(x * y) };
        }

        public static bool Verify(byte[] publicKey, byte[] message, byte[] signature) {
            if (publicKey == null || publicKey.Length != 32 || signature == null || signature.Length != 64) { return false; }
            BigInteger[] a = Decompress(publicKey, 0);
            if (a == null) { return false; }
            BigInteger[] r = Decompress(signature, 0);
            if (r == null) { return false; }
            BigInteger s = FromLittleEndian(signature, 32, 32);
            if (s >= L) { return false; }
            byte[] input = new byte[64 + message.Length];
            Array.Copy(signature, 0, input, 0, 32);
            Array.Copy(publicKey, 0, input, 32, 32);
            Array.Copy(message, 0, input, 64, message.Length);
            byte[] digest;
            using (SHA512 sha = SHA512.Create()) { digest = sha.ComputeHash(input); }
            BigInteger h = FromLittleEndian(digest, 0, 64) % L;
            return Equal(Multiply(s, G), Add(r, Multiply(h, a)));
        }
    }
}
'@
    }

    function ConvertFrom-RawBase64([string]$Encoded) {
        $text = $Encoded.Trim()
        switch ($text.Length % 4) {
            2 { $text += '==' }
            3 { $text += '=' }
        }
        return [Convert]::FromBase64String($text)
    }

    function Test-DownloadedRelease([string]$Directory, [string[]]$Names) {
        $manifestPath = Join-Path $Directory 'release-manifest.json'
        $signaturePath = Join-Path $Directory 'release-manifest.json.sig'
        $manifestBytes = [IO.File]::ReadAllBytes($manifestPath)
        $signatureText = [IO.File]::ReadAllText($signaturePath).Trim()
        if (-not $ReleasePublicKey -or -not $signatureText) {
            $targetDev = $script:TargetVersion -and $script:TargetVersion.dev
            if (-not ($targetDev -and $env:OBOARD_ALLOW_UNSIGNED_DEV_UPDATE -eq '1')) {
                throw '缺少 release 公钥或签名，拒绝安装未验证的 Agent。'
            }
            Write-Host '开发模式：跳过 unsigned manifest 校验。'
        } else {
            Initialize-ReleaseVerifier
            if (-not [OBoard.ReleaseVerifier]::Verify((ConvertFrom-RawBase64 $ReleasePublicKey), $manifestBytes, (ConvertFrom-RawBase64 $signatureText))) {
                throw 'Agent 发布清单签名校验失败，已停止安装。'
            }
        }
        $manifest = [Text.Encoding]::UTF8.GetString($manifestBytes) | ConvertFrom-Json
        foreach ($name in $Names) {
            $entry = $manifest.files | Where-Object { $_.name -eq $name -and $_.os -eq $OsValue -and $_.arch -eq $ArchValue } | Select-Object -First 1
            if (-not $entry) { throw "发布清单中没有 $name" }
            $path = Join-Path $Directory $name
            $hash = (Get-FileHash -LiteralPath $path -Algorithm SHA256).Hash.ToLowerInvariant()
            $size = (Get-Item -LiteralPath $path).Length
            if ($hash -ne $entry.sha256 -or $size -ne [int64]$entry.size) {
                throw "$name 校验失败"
            }
        }
        return $manifest
    }

    function Get-TargetVersion {
        try {
            $version = Invoke-RestMethod -UseBasicParsing -Uri "$BaseUrl/api/v1/ui/version" -TimeoutSec 30
        } catch {
            return $null
        }
        return $version
    }

    function Get-ServiceState([string]$Name) {
        $service = Get-Service -Name $Name -ErrorAction SilentlyContinue
        if (-not $service) { return $null }
        return $service.Status
    }

    function Wait-ServiceStable([string]$Name, [int]$Seconds = 15) {
        $stable = 0
        for ($elapsed = 0; $elapsed -lt $Seconds; $elapsed++) {
            if ((Get-ServiceState $Name) -eq 'Running') {
                $stable++
                if ($stable -ge 3) { return $true }
            } else {
                $stable = 0
            }
            Start-Sleep -Seconds 1
        }
        return $false
    }

    function Stop-ManagedService([string]$Name) {
        if (-not (Get-ServiceState $Name)) { return }
        Stop-Service -Name $Name -Force -ErrorAction SilentlyContinue
        (Get-Service -Name $Name).WaitForStatus('Stopped', [TimeSpan]::FromSeconds(30))
    }

    function Stop-ManagedRealm {
        Get-Process -Name 'oboard-realm' -ErrorAction SilentlyContinue |
            Where-Object { $_.Path -and ($_.Path -ieq $RealmExe) } |
            Stop-Process -Force -ErrorAction SilentlyContinue
    }

    function Register-ManagedService([string]$Name, [string]$DisplayName, [string]$Description, [string]$BinaryPath) {
        $sc = Join-Path $env:SystemRoot 'System32\sc.exe'
        if (Get-ServiceState $Name) {
            if ((Invoke-Native $sc @('config', $Name, 'binPath=', $BinaryPath, 'start=', 'auto')) -ne 0) {
                throw "无法更新服务 $Name"
            }
        } else {
            New-Service -Name $Name -BinaryPathName $BinaryPath -DisplayName $DisplayName -Description $Description -StartupType Automatic | Out-Null
        }
        # Restart after any failure, including a non-zero exit, like
        # Restart=always under systemd.
        [void](Invoke-Native $sc @('failure', $Name, 'reset=', '86400', 'actions=', 'restart/5000/restart/5000/restart/10000'))
        [void](Invoke-Native $sc @('failureflag', $Name, '1'))
    }

    function Register-ManagedServices {
        # Managed paths never contain spaces, so the service command lines
        # need no quoting.
        $coreCommand = "$CoreExe -config $(Join-Path $StateDir 'sing-box.json') -api unix:$CoreSocket -log-file $(Join-Path $LogDir 'oboard-sb.log')"
        $agentCommand = "$AgentExe -config $ConfigPath -log-file $(Join-Path $LogDir 'oboard-agent.log')"
        Register-ManagedService $CoreService 'OBoard sing-box kernel' 'OBoard optimized sing-box kernel' $coreCommand
        Register-ManagedService $AgentService 'OBoard Agent' 'OBoard node Agent' $agentCommand
    }

    function Install-Components {
        Write-Step '[2/4] 下载 Agent 组件'
        $tmp = Join-Path $StateDir ('update-' + [Guid]::NewGuid().ToString('N'))
        New-Item -ItemType Directory -Force -Path $tmp | Out-Null
        try {
            $agentName = "oboard-agent-$OsValue-$ArchValue"
            $coreName = "oboard-sb-$OsValue-$ArchValue"
            $realmName = "oboard-realm-$OsValue-$ArchValue"
            Invoke-Download 'Agent' "$BaseUrl/downloads/$agentName" (Join-Path $tmp $agentName)
            Invoke-Download '优化内核' "$BaseUrl/downloads/$coreName" (Join-Path $tmp $coreName)
            Invoke-Download '端口转发组件' "$BaseUrl/downloads/$realmName" (Join-Path $tmp $realmName)
            Invoke-WebRequest -UseBasicParsing -Uri "$BaseUrl/downloads/release-manifest.json" -OutFile (Join-Path $tmp 'release-manifest.json') -TimeoutSec 60
            Invoke-WebRequest -UseBasicParsing -Uri "$BaseUrl/downloads/release-manifest.json.sig" -OutFile (Join-Path $tmp 'release-manifest.json.sig') -TimeoutSec 60
            Write-Step '[3/4] 校验并安装组件'
            [void](Test-DownloadedRelease $tmp @($agentName, $coreName, $realmName))
            $stagedAgent = Join-Path $tmp 'oboard-agent.exe'
            $stagedCore = Join-Path $tmp 'oboard-sb.exe'
            $stagedRealm = Join-Path $tmp 'oboard-realm.exe'
            Move-Item -LiteralPath (Join-Path $tmp $agentName) -Destination $stagedAgent
            Move-Item -LiteralPath (Join-Path $tmp $coreName) -Destination $stagedCore
            Move-Item -LiteralPath (Join-Path $tmp $realmName) -Destination $stagedRealm
            # Validate the downloaded kernel against the configuration this
            # node is serving before anything on disk is replaced.
            $activeConfig = Join-Path $StateDir 'sing-box.json'
            if ((Test-Path -LiteralPath $activeConfig) -and (Get-Item -LiteralPath $activeConfig).Length -gt 0) {
                Write-Step '校验新版内核是否接受当前运行的配置'
                if ((Invoke-Native $stagedCore @('-check', '-config', $activeConfig)) -ne 0) {
                    throw "新版内核无法接受当前正在运行的配置，已中止更新，未替换任何文件。详细信息见 $script:InstallLog。"
                }
            }
            # A running image can be renamed but not overwritten. Move each
            # live binary aside, then move the verified one into place; the
            # old copy is removed once nothing runs it any more.
            foreach ($pair in @(@($stagedAgent, $AgentExe), @($stagedCore, $CoreExe), @($stagedRealm, $RealmExe))) {
                $target = $pair[1]
                $backup = "$target.old"
                Remove-Item -LiteralPath $backup -Force -ErrorAction SilentlyContinue
                if (Test-Path -LiteralPath $target) {
                    Move-Item -LiteralPath $target -Destination $backup -Force
                }
                Move-Item -LiteralPath $pair[0] -Destination $target -Force
            }
        } finally {
            Remove-Item -LiteralPath $tmp -Recurse -Force -ErrorAction SilentlyContinue
        }
    }

    function Remove-ReplacedBinaries {
        foreach ($target in @($AgentExe, $CoreExe, $RealmExe)) {
            Remove-Item -LiteralPath "$target.old" -Force -ErrorAction SilentlyContinue
        }
    }

    function Test-InstalledVersions($Target) {
        if (-not $Target) { return }
        $build = [string]$Target.agent_expected_build
        if ($build) {
            $agentVersion = Invoke-NativeOutput $AgentExe @('-version')
            Write-InstallLog "Agent: $agentVersion"
            if ($agentVersion -notmatch [regex]::Escape("build $build")) {
                throw '安装的 Agent 二进制 build 与目标 build 不一致，操作未完成。请重新执行命令。'
            }
        }
        $coreBuild = if ($Target.kernel_build) { [string]$Target.kernel_build } else { $build }
        if ($coreBuild) {
            $coreVersion = Invoke-NativeOutput $CoreExe @('-version')
            Write-InstallLog "Kernel: $coreVersion"
            if ($coreVersion -notmatch [regex]::Escape('"build": "' + $coreBuild + '"')) {
                throw '安装的优化内核 build 与目标 build 不一致，操作未完成。请重新执行命令。'
            }
        }
    }

    function Test-CoreRuntime {
        if (-not (Test-Path -LiteralPath (Join-Path $StateDir 'sing-box.json'))) { return }
        $arguments = @('-verify-core-runtime', '-config', $ConfigPath, '-state-dir', $StateDir, '-core-binary', $CoreExe, '-core-service', $CoreService)
        if ((Invoke-Native $AgentExe $arguments) -ne 0) {
            throw "内核已重启，但运行中的进程仍不是本次安装的版本或配置，更新未完成。详细信息见 $script:InstallLog。"
        }
    }

    function Resolve-UpdatePolicy($Target) {
        $allow = $AllowPanelUpdate
        if (-not $allow) {
            $allow = if ($Target -and $Target.dev) { '1' } else { '0' }
        }
        $allowBool = $allow -in @('1', 'true', 'TRUE', 'yes', 'YES', 'on', 'ON')
        $source = $UpdateSource
        if (-not $source) { $source = if ($allowBool) { 'panel' } else { 'github' } }
        return @{ Allow = $allowBool; Source = $source }
    }

    function Uninstall-Agent {
        Write-Step '停止并删除 OBoard 服务'
        $sc = Join-Path $env:SystemRoot 'System32\sc.exe'
        foreach ($name in @($AgentService, $CoreService)) {
            if (Get-ServiceState $name) {
                Stop-ManagedService $name
                [void](Invoke-Native $sc @('delete', $name))
            }
        }
        Stop-ManagedRealm
        Start-Sleep -Seconds 2
        if ($Purge) {
            Remove-Item -LiteralPath $Root -Recurse -Force -ErrorAction SilentlyContinue
            Write-Host 'OBoard Agent、oboard-sb 和本机配置已卸载。'
        } else {
            Remove-Item -LiteralPath $BinDir -Recurse -Force -ErrorAction SilentlyContinue
            Remove-Item -LiteralPath $RunDir -Recurse -Force -ErrorAction SilentlyContinue
            Write-Host "OBoard Agent 与 oboard-sb 已卸载，配置与状态保留在 $Root。"
        }
    }

    if ($Action -eq 'uninstall') {
        Uninstall-Agent
        return
    }

    Write-Host 'OBoard Agent'
    Write-Host '------------'
    Write-Host "主控地址：$BaseUrl"
    Write-Host "安装目录：$Root"
    Write-Host ''
    Write-Step '[1/4] 检查运行环境'
    Protect-Root
    Write-InstallLog "==== $(Get-Date -Format o) $Action windows/$ArchValue ===="
    if ($env:OBOARD_INSTALL_BBR -eq '1' -or $env:OBOARD_INSTALL_TCP_TUNING -eq '1') {
        Write-Host 'BBR 与 TCP 调优仅适用于 Linux 服务器，Windows 安装已跳过。'
    }
    $target = Get-TargetVersion
    $script:TargetVersion = $target

    if ($Action -eq 'install') {
        if (-not $EnrollToken) {
            throw '安装 Agent 需要面板生成的一次性安装令牌。请先在主控面板添加服务器，再复制该服务器的 Windows 安装命令。'
        }
        Enter-CoreLifecycleLock
        try {
            foreach ($name in @($AgentService, $CoreService)) { Stop-ManagedService $name }
            Stop-ManagedRealm
            Install-Components
            Register-ManagedServices
            Write-Step '[4/4] 注册并启动 Agent 服务'
            $policy = Resolve-UpdatePolicy $target
            $allowFlag = if ($policy.Allow) { 'true' } else { 'false' }
            $env:OBOARD_ENROLL_TOKEN = $EnrollToken
            try {
                $code = Invoke-Native $AgentExe @('-config', $ConfigPath, '-controller', $BaseUrl, '-state-dir', $StateDir, '-core-binary', $CoreExe, '-core-service', $CoreService, '-update-source', $policy.Source, "-allow-panel-update=$allowFlag", '-update-repo', $UpdateRepo, '-enroll-only')
            } finally {
                Remove-Item Env:\OBOARD_ENROLL_TOKEN -ErrorAction SilentlyContinue
            }
            if ($code -ne 0) {
                throw "Agent 未能连接主控完成注册，请确认主控地址和安装令牌后重试。详细信息见 $script:InstallLog。"
            }
        } finally {
            Exit-CoreLifecycleLock
        }
        Start-Service -Name $AgentService
        if (-not (Wait-ServiceStable $AgentService 15)) {
            throw "Agent 服务未能保持运行，详细信息见 $(Join-Path $LogDir 'oboard-agent.log')。"
        }
        Remove-ReplacedBinaries
        Test-InstalledVersions $target
        Write-Host ''
        Write-Host '安装完成：Agent 已注册并在后台运行。'
        Write-Host '提示：oboard-sb 会在面板首次下发配置后自动启动。'
        Write-Host "日志目录：$LogDir"
        return
    }

    # update
    if (-not (Test-Path -LiteralPath $ConfigPath) -or -not (Test-Path -LiteralPath $AgentExe)) {
        throw '未找到已安装的 Agent 配置和二进制文件，无法执行更新。需要恢复离线服务器时，请重新获取接入命令执行安装。'
    }
    Enter-CoreLifecycleLock
    try {
        Install-Components
        Register-ManagedServices
        Write-Step '[4/4] 刷新 Agent 服务'
        Stop-ManagedRealm
        if ((Test-Path -LiteralPath (Join-Path $StateDir 'sing-box.json'))) {
            Stop-ManagedService $CoreService
            Start-Service -Name $CoreService
            if (-not (Wait-ServiceStable $CoreService 15)) {
                throw "内核 oboard-sb 重启后未能保持运行，更新未完成。详细信息见 $(Join-Path $LogDir 'oboard-sb.log')。"
            }
            Test-CoreRuntime
        }
    } finally {
        # The Agent restarts after the lock is released so its first task on
        # reconnect does not collide with this installer still holding it.
        Exit-CoreLifecycleLock
    }
    Stop-ManagedService $AgentService
    Start-Service -Name $AgentService
    if (-not (Wait-ServiceStable $AgentService 15)) {
        throw "Agent 重启后未能保持运行，更新未完成。详细信息见 $(Join-Path $LogDir 'oboard-agent.log')。"
    }
    Remove-ReplacedBinaries
    Test-InstalledVersions $target
    Write-Host ''
    Write-Host '更新完成：Agent 与内核已切换到新版本。'
}

try {
    Invoke-OBoardAgentInstaller
} catch {
    Write-Host ''
    Write-Host ('操作未完成：' + $_.Exception.Message) -ForegroundColor Red
    if ($script:LockStream) {
        try { $script:LockStream.Dispose() } catch { }
    }
    $global:LASTEXITCODE = 1
}
