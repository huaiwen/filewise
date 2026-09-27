# Install and upgrade / 安装与升级

## 中文

### 普通用户：安装后直接运行

无需克隆仓库、Rust、Python、Node 或 C 编译器。安装器只下载 GitHub Release 中的原生程序，**不会在本机编译**。

```bash
curl -fsSL https://github.com/huaiwen/filewise/releases/latest/download/install.sh | bash
export PATH="$HOME/.local/bin:$PATH"
filewise start
```

安装需要 Bash、curl、tar、gzip，以及 `sha256sum` 或 `shasum`；macOS 通常已自带，精简 Linux 环境可能需补齐。默认安装位置是 `~/.local/bin/filewise`，不使用 sudo，不改 shell 配置、不启动服务或登录项。上面的 PATH 设置仅影响当前终端；要长期使用，可自行把该 `export` 行加入自己的 shell 配置，或直接运行 `~/.local/bin/filewise start`。

已下载源码时，在仓库根目录运行 `bash install.sh` 即可。它安装**最新已发布版本**，不构建当前分支，也不包含尚未发布的改动。

发布流程提供以下构建目标；实际可下载的平台以对应 Release 的附件为准：

| 系统 / 架构 | 安装包 | 构建基线 |
| --- | --- | --- |
| macOS Apple Silicon | `filewise-aarch64-apple-darwin.tar.gz` | macOS 13+ |
| macOS Intel | `filewise-x86_64-apple-darwin.tar.gz` | macOS 13+ |
| Linux Intel/AMD 64 位 | `filewise-x86_64-unknown-linux-gnu.tar.gz` | glibc 2.35+（Ubuntu 22.04 构建） |
| Linux ARM64 | `filewise-aarch64-unknown-linux-gnu.tar.gz` | glibc 2.35+（Ubuntu 22.04 构建） |

Windows 和 Alpine/musl 暂无原生安装包。macOS 包尚未使用 Apple 开发者签名与公证；若被系统拦截，先核对下载来源和校验值，再按“系统设置 → 隐私与安全性”的提示允许该程序。安装器不关闭 Gatekeeper，也不删除隔离标记。

### 指定版本、手动下载

从 [Releases](https://github.com/huaiwen/filewise/releases) 下载该版 `install.sh`，可先查看脚本，再运行：

```bash
# 把版本号替换为 Releases 中实际存在的版本；也支持显式指定预发布版本。
bash install.sh v0.2.0
# 可选：改变安装位置，必须是绝对路径。
FILEWISE_INSTALL_DIR="$HOME/bin" bash install.sh v0.2.0
```

不带版本号时使用 GitHub 的 latest Release（不包含预发布）。安装器先固定版本，再下载对应架构的压缩包及 `.sha256`；校验、解包和版本检查都通过后才替换旧程序。下载失败、校验不符或系统不支持时保留原安装。已有目标为符号链接时拒绝覆盖，避免破坏包管理器维护的文件。

也可以手动下载平台 `.tar.gz` 和同名 `.tar.gz.sha256`，在下载目录执行 `shasum -a 256 -c 包名.tar.gz.sha256`（Linux 可用 `sha256sum -c`），通过后解压并运行 `./filewise start`。SHA-256 检查文件完整性；校验文件来自同一 GitHub Release，并非独立签名。

### 升级与卸载

升级前先停止服务，再重跑安装器；配置、文件历史和原始文件不受安装器影响。使用自定义 `--db` 的实例也要分别停止，避免旧服务与新解析子进程混用。

```bash
filewise stop
curl -fsSL https://github.com/huaiwen/filewise/releases/latest/download/install.sh | bash
filewise start
```

卸载时先停止各实例；启用了 macOS 登录启动的实例，先对同一 `--db` 执行 `filewise autostart --disable`。然后删除所安装的 `filewise` 可执行文件即可。数据库和历史不会被删除。

### 开发者：从源码构建

只有修改代码、测试未发布改动时才需要 Rust 1.86+ 和 C 编译工具链：

```bash
git clone https://github.com/huaiwen/filewise.git
cd filewise
cargo test --locked
cargo build --locked --release
./target/release/filewise start
```

安装器的离线检查：`bash tests/install.sh`；检查真实二进制的安装：`bash tests/install.sh "$PWD/target/release/filewise"`。测试使用临时 HOME、安装目录和模拟下载，不访问真实用户配置。CI 在每次 push / PR 执行安装检查。

### 维护者：自动构建与发布

`.github/workflows/release.yml` 在四种原生 runner 上测试、构建并打包，不在用户电脑上编译。手动运行 **Native binaries** 工作流只生成 Actions 附件；推送与 `Cargo.toml` 版本一致的 `vX.Y.Z` 标签才会发布 GitHub Release。所有平台测试通过后上传四个压缩包、四个校验文件、安装脚本及其校验文件。预发布标签标为 prerelease，不进入 latest。

发布前核对版本号与测试结果，再推送对应标签。现有 Release 不被覆盖；上传失败的草稿留待维护者检查。

## English

### Users: install, then run

No clone, Rust, Python, Node or C compiler is needed. The installer downloads a native GitHub Release, verifies SHA-256, checks the executable version, then installs to `~/.local/bin/filewise` without sudo. It never compiles, edits shell profiles, starts services or enables login startup.

```bash
curl -fsSL https://github.com/huaiwen/filewise/releases/latest/download/install.sh | bash
export PATH="$HOME/.local/bin:$PATH"
filewise start
```

Requires Bash, curl, tar, gzip and either sha256sum or shasum. Add the PATH line to your own shell configuration if desired, or use `~/.local/bin/filewise` directly. If you already cloned the repository, run `bash install.sh`: it installs the latest published binary, not your checkout's uncommitted/unreleased changes.

Build targets: macOS 13+ on Apple Silicon/Intel, and Linux glibc 2.35+ on x86-64/ARM64 (Ubuntu 22.04 build baseline). See the table above for asset names; available platforms are the assets actually attached to each release. No native Windows or Alpine/musl package. macOS binaries are not Apple Developer ID signed/notarized. If macOS blocks execution, verify the source/checksum and follow System Settings → Privacy & Security; the installer does not disable Gatekeeper or remove quarantine attributes.

### Versions and upgrades

Download and inspect `install.sh` from [Releases](https://github.com/huaiwen/filewise/releases), then use `bash install.sh v0.2.0` for a specific published version (including prereleases). Set `FILEWISE_INSTALL_DIR` to an absolute path for a different destination. With no version argument, GitHub's latest non-prerelease is selected once before downloading. A failed download/check leaves the existing binary intact; symlink destinations are refused.

For manual installation, download the platform archive and its `.sha256` file, verify with `shasum -a 256 -c ARCHIVE.sha256` (or `sha256sum -c`), extract, and run `./filewise start`. Checksums protect integrity, not against compromise of the publishing account; they are not independent signatures.

**Stop all instances before upgrading**, rerun the installer, then restart. Custom `--db` instances need their own stop/start. Data and history remain untouched. To uninstall, stop all instances, disable any configured macOS login startup with `autostart --disable` for each relevant DB, then remove only the executable.

### Developers: build from source

Only source changes require Rust 1.86+ and a C toolchain. Clone the repo, run `cargo test --locked`, `cargo build --locked --release`, then `./target/release/filewise start`. Offline installer checks: `bash tests/install.sh`; add the absolute native binary path as the argument to test installing a real build.

**Maintainers:** the Native binaries workflow builds/tests four native targets. Manual branch runs upload Actions artifacts only; a version tag matching Cargo.toml publishes a Release after every target passes. Prerelease tags stay out of latest; existing releases are not overwritten. Verify the version and test results before pushing a release tag; failed uploads leave a draft for inspection.
