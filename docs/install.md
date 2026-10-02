# Install and upgrade / 安装与升级

## 中文

### 当前 Go 源码版

当前版本 **`0.3.0-dev (Go)` 尚未公开发行**。在当前源码目录构建并启动：

```bash
make build
./build/filewise --version
./build/filewise start
```

需要 Go 1.27+；普通构建使用 `CGO_ENABLED=0`，不需要 Rust、Python、Node 或 C 编译器。产物为 `build/filewise`，不会覆盖用户已安装程序。`make check` 的 race 检查另需 C 编译工具链。

默认 Go 状态独立保存于 macOS 的 `~/Library/Application Support/Filewise-Go/` 或 Linux 的 `~/.local/share/filewise-go/`。原有数据库不转换、不接管；首次接入从当前观察状态开始记录。底层项目命令默认 `.filewise-go/filewise.db`；混用命令时明确传入同一个 `--db`。

### Go 二进制发行后

从对应的 [Release](https://github.com/huaiwen/filewise/releases) 下载并检查 `install.sh`，指定实际已发布的 Go 标签：

```bash
# 将提示输入替换为 Release 中确实存在的 Go 标签。
printf 'Published Go tag: '
read -r GO_TAG
bash install.sh "$GO_TAG"
export PATH="$HOME/.local/bin:$PATH"
filewise start
```

本地安装器默认选择 GitHub latest，但会拒绝 `v0.2.x` 等历史版本，不会退回 Rust 或现场编译。当前没有 Go Release 时，使用源码构建。历史 [v0.2.0](https://github.com/huaiwen/filewise/releases/tag/v0.2.0) 的资产和安装脚本保持原样，不是 Go 版本。

安装器需要 Bash、curl、tar、gzip，以及 `sha256sum` 或 `shasum`。默认目录 `~/.local/bin`；用绝对路径 `FILEWISE_INSTALL_DIR` 自定义。流程固定版本、校验 SHA-256、只提取单个预期文件、确认版本带 `(Go)`，最后同目录替换。失败保留已有程序，拒绝符号链接目标；不使用 sudo，不改配置，不启动服务或登录项。

| 目标 | Go 发行包名 |
| --- | --- |
| macOS Apple Silicon | `filewise-darwin-arm64.tar.gz` |
| macOS Intel | `filewise-darwin-amd64.tar.gz` |
| Linux ARM64 | `filewise-linux-arm64.tar.gz` |
| Linux x86-64 | `filewise-linux-amd64.tar.gz` |

四目标均关闭 CGO；交叉构建不等于已在对应系统运行。Windows 未实现，最低系统版本、Alpine/musl 和其他设备尚需实机验证。macOS 程序尚未 Apple 签名/公证；安装器不会关闭系统保护。

手动下载时同时获取压缩包及 `.sha256`，运行 `shasum -a 256 -c ARCHIVE.sha256`（或 `sha256sum -c`）后解压。校验文件来自同一 Release，保护完整性，不是独立签名。第三方许可已嵌入二进制，用 `filewise --licenses` 查看。

### 升级与卸载

替换程序前停止**所有**实例，包括自定义 `--db` 的实例，避免旧服务与新解析子进程混用。先备份各自的私有状态目录和原文件目录，再替换程序。Go 不会升级旧版状态。

```bash
filewise stop
# 安装已发布的 Go 版本，或替换为已验证的本地 Go 构建。
filewise start
```

卸载时停止实例，对启用登录启动的同一数据库执行 `filewise autostart --disable`，再删除可执行文件。数据库、历史和原件由用户保留。

### 开发者：从源码构建

```bash
make check
make build
FILEWISE_TEST_BINARY="$PWD/build/filewise" npm test
bash tests/install.sh "$PWD/build/filewise"
```

npm/网站检查需要 Node.js 22+；安装检查使用临时 HOME、模拟网络和真实本地 Go 二进制，不会安装到用户目录。Dockerfile 使用 Go 构建和非 root 运行，凭据作为私有只读挂载提供；容器不是自动的 Agent 授权方案。

### 维护者：构建与发布

`.github/workflows/release.yml` 配置 macOS/Linux × ARM64/x86-64 四种原生 runner。手动运行只生成 Actions 附件；经单独授权后推送与 Go `--version` 及 `package.json.filewise.nativeVersion` 一致的新标签，才发布四个压缩包/校验文件及安装脚本/校验文件。预发布标签不进入 latest，已有 Release 不覆盖，失败草稿保留检查。

源码推送触发 [Go checks](https://github.com/huaiwen/filewise/actions/workflows/ci.yml)，不发布 Go/npm 包或部署网站。验证结果对应具体提交；历史 Rust CI 不能代替 Go 验证。

## English

### Current Go source build

**`0.3.0-dev (Go)` is not publicly released.** From the current checkout, run `make build`, then `./build/filewise start`. Go 1.27+ is required; normal builds disable CGO and require no Rust, Python, Node or C compiler. Race-detector tests additionally need a C toolchain. The build stays under `build/` and does not replace an installed executable.

Go uses separate state under `~/Library/Application Support/Filewise-Go/` on macOS or `~/.local/share/filewise-go/` on Linux. Lower-level project commands default to `.filewise-go/filewise.db`; pass the same explicit `--db` when combining interfaces. Legacy databases are not imported or rewritten.

### Published Go binaries

After a Go release exists, inspect its installer and run `bash install.sh PUBLISHED_GO_TAG`. This branch's installer rejects historical Rust releases and requires a matching `(Go)` version response. It never compiles or silently falls back to v0.2.0. The immutable historical release remains available separately.

Requires Bash, curl, tar, gzip and a SHA-256 tool. Default destination: `~/.local/bin`; `FILEWISE_INSTALL_DIR` must be absolute. Checksums, exact archive membership and runtime identity are checked before replacement. No sudo, profile changes, service startup or login enrollment. `filewise --licenses` prints embedded third-party notices. Checksum files are not independent signatures.

Targets and asset names are in the table above. Go binaries are CGO-disabled. Cross-build success does not certify execution on another OS; minimum-OS, musl and physical-device testing remain separate. macOS binaries are not Apple-notarized.

### Developers: build from source

Run the development commands above; Node.js 22+ is used only for distribution/UI checks. The Docker configuration builds Go and runs non-root. Stop every instance before replacing its executable, back up state and originals, then restart. To uninstall, stop instances, disable any opted-in login item for the same DB, and remove only the executable.

Source pushes trigger Go checks, not package publication or site deployment. Manual release-workflow runs produce artifacts only; an authorized matching new tag can publish after all four native gates pass. Existing tags and release assets remain immutable. Check the Go CI run for the corresponding commit rather than using historical Rust results.
