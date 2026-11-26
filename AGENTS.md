# LanSync · 项目交接说明（给 AI 助手）

> 这份文件是给接手这个项目的 AI 助手读的。用户是中文用户，Windows 环境，非专业开发者。
> 接手前请先读完本文，不要凭直觉改协议或重写架构。

## 1. 这个项目是什么

局域网内把手机照片/视频**自动**同步到电脑的私有工具，对标 Syncthing 但只做单向（手机 → 电脑）。

- **不做**：多设备互同步、双向、冲突合并、版本历史、公网穿透、账号体系
- **Docker/服务器？** 不需要。电脑端就是一个单文件 exe，双击跑起来就完事
- 当前版本 v1.1.0，双端已实现并编译通过

## 2. 技术栈与产物

| 端 | 语言/框架 | 产物 |
|---|---|---|
| 电脑端 | Go 1.21+，标准库 + `github.com/skip2/go-qrcode` | `dist/LanSync.exe`（5.3MB，`-H windowsgui` 无窗口） |
| 手机端 | Kotlin 1.9 / AGP 8.x，minSdk 26 / target 34 | `dist/LanSync.apk`（debug 签名，6.6MB） |
| 界面 | 单文件 HTML + 原生 JS，`go:embed` 内嵌进 exe | `server/web/index.html` |

## 3. 目录地图

```
server/
  main.go        服务入口、路由分发、鉴权、二维码、Windows 通知、开机自启、局域网 IP 探测
  store.go       落盘逻辑：路径净化、原子写入、SHA-256、index.json 索引
  dav.go         WebDAV 兜底通道（给 FolderSync / 资源管理器用）
  web/index.html 看板界面（内嵌，改完必须重新编译 exe）
app/app/src/main/
  java/com/lansync/app/
    Config.kt        SharedPreferences 存 host/port/token，baseUrl 拼装
    Api.kt           OkHttp 封装：check / put 流式上传
    MediaScanner.kt  扫 MediaStore 拿照片视频清单
    SyncEngine.kt    核对 + 逐个上传的主流程（协程）
    SyncWorker.kt    WorkManager 定时任务（连 Wi-Fi 才跑）
    SyncService.kt   前台服务，防被系统回收
    BootReceiver.kt  开机重挂任务
    MainActivity.kt  扫码配对 + 三个按钮
  res/layout/activity_main.xml   手机界面
```

## 4. 协议契约（**改一端必须同步改另一端**）

所有请求走 HTTP，默认端口 8765（被占用自动 +1）。

**鉴权三种等价方式**，任一通过即可：
- 请求头 `X-Token: <token>`
- URL 参数 `?t=<token>`（用于扫码进入网页时种 Cookie）
- Cookie `lst=<token>`（网页看板用）
- WebDAV 走 HTTP Basic，用户名随意、密码填 token

**二维码载荷**：`http://<IP>:<PORT>/?t=<TOKEN>`
App 端解析这个 URL 取 host / port / token，扫码一次永久保存。

| 接口 | 方法 | 鉴权 | 入参 | 出参 |
|---|---|---|---|---|
| `/api/ping` | GET | **否** | — | `{ok,name,ver}`（供单实例探测、App 判断在线） |
| `/api/info` | GET | 是 | — | `{ok,name,ver,host,ips:[{ip,iface,note}],port,url,dav,token,...}` |
| `/api/check` | POST | 是 | `{"items":[{"path":"2026-11-26/a.jpg","size":123,"mtime":1795690000}]}` | `{ok,need:[路径],have:12,total:3000}` |
| `/api/put?path=<urlencoded>` | PUT | 是 | Body 为文件二进制，头 `X-Mtime: <unix秒>` | `{ok,path,size,dup,sha256}` |
| `/api/list?limit=N` | GET | 是 | — | 文件列表 |
| `/api/delete` | POST | 是 | `{"path":"..."}` | `{ok}` |
| `/api/quit` | POST | 是 | — | 触发进程退出 |
| `/qr.png` | GET | 否 | 可带 `?ip=` 覆盖 | PNG 二维码 |
| `/dav/*` | PROPFIND/MKCOL/PUT/GET/MOVE/DELETE/OPTIONS/LOCK | Basic | — | WebDAV 标准响应 |

**核对逻辑（核心，别改坏）**：`size` 相等 **且** `|mtime 差| <= 5 秒` 才算已存在，否则进 `need`。
路径一律先过 `sanitizeRel()` 再查索引，`..` 直接判为待传输（后续 PUT 会被 `absPath` 拦住）。

## 5. 关键实现约束（前人踩过的坑，别重犯）

| 位置 | 约束 | 原因 |
|---|---|---|
| `main.go: main` | 启动时必须先 `findRunning(cfg.Port)` 做单实例检测，命中就打开看板后退出 | 否则每双击一次就多一个进程、端口 +1，多个端口同时生效 |
| `main.go: runPort` | 运行时一律用 `runPort`，`cfg.Port` 只表示「首选端口」，**避让结果绝不写回配置** | 一旦写回，8765 被占一次后就永久从 8766 起，端口无限爬升 |
| `main.go: /api/ping` | 必须免鉴权，且在 `/api/` 鉴权分支**之前**处理 | App 和单实例探测都依赖它，带令牌会形成循环依赖 |
| `main.go: candidateIPs` | 虚拟网卡减分并标注；有正常地址时丢弃 169.254；`lanIP()` 取列表第一项 | 电脑多网卡时二维码可能指向手机到不了的地址 |
| `store.go: writeIncoming` | 必须流式 64KB 拷贝，写 `.part` 后 `os.Rename` 原子改名 | 大视频会撑爆内存；断网不能留半个坏文件 |
| `store.go: sanitizeRel` | 逐段校验，禁 `\ / : * ? " < > \|`，尾部空格和点要去掉 | Windows 非法文件名；防路径穿越 |
| `main.go: isLocal` | 本机回环访问免 token | 否则双击 exe 打开看板会被自己拦 |
| `main.go: toast` | PowerShell 调 WinRT，参数必须转义单引号 | 中文路径/文件名带引号会炸 |
| `main.go: ensureAutostart` | 写 `HKCU\...\Run`，`reg add /f` | 不需要管理员权限 |
| `dav.go` | 目录 href **必须**以 `/` 结尾；`LOCK` 返回假 200 | 否则部分客户端不认目录，同步直接失败 |
| `handlePut` | 先 `sanitizeRel` 再回显路径 | 否则报错信息里会泄漏净化前路径 |
| `SyncWorker.kt` | 约束必须是 `NetworkType.CONNECTED`，**不能用 UNMETERED** | 手机开热点时 Wi-Fi 是关的，系统判定为计费蜂窝网，UNMETERED 永不满足，后台任务根本不启动 |
| 看板 HTML | 改完必须 `go build` 重新生成 exe | HTML 是 `go:embed` 编译进去的 |

## 6. 构建命令

**电脑端**（在 `server/` 目录）：
```bash
# Windows 原生
go build -trimpath -ldflags "-s -w -H windowsgui" -o ../dist/LanSync.exe .

# 从 Linux/macOS 交叉编译
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath \
  -ldflags "-s -w -H windowsgui" -o ../dist/LanSync.exe .
```
去掉 `-H windowsgui` 就是带控制台的排错版，发布时两个都要出。

**手机端**（在 `app/` 目录）：
```bash
# 需要 JDK 17 + Android SDK (platform 34, build-tools 34.0.0)
# local.properties 里写 sdk.dir=...
gradle assembleDebug
# 产物 app/build/outputs/apk/debug/app-debug.apk
```
或直接 push 到 GitHub，`.github/workflows/build-apk.yml` 自动出 APK。

## 7. 已知状态与待办

**已验证**：协议全链路（错误 token 拦截 → 首次核对全待传 → 上传 → 二次核对全跳过）、mtime 保留、路径穿越拦截、WebDAV 的 MKCOL/PUT/MOVE/GET、桌面与手机两种宽度下的界面排版。

**v1.1.0 修的四件事**（都已在 Linux 侧实测）：
1. 单实例：第二次启动检测到已有实例，打印提示、打开看板、退出码 0，进程数与端口均不变
2. 端口不再爬升：占用 8765 时运行在 8766，但 `config.json` 仍写 8765，释放后自动回到 8765
3. `ping` 免鉴权；`/api/info` 增加 `ips` 候选地址列表，看板可点选切换二维码
4. 手机端 `NetworkType` 改为 `CONNECTED`，热点场景后台任务能正常触发；探测超时压到 2 秒
5. 顺带：日志同时输出到 stdout（控制台版终于能看到实时日志）；看板明确提示「关网页不停同步」

**未验证（重要）**：
1. **APK 从未在真机跑过** — 沙箱无 Android 设备。扫码、MediaStore 读取、后台通知、热点场景下的自动同步都需要真机确认
2. **Windows 专有路径未在 Windows 上执行** — Toast 通知（PowerShell/WinRT）、开机自启（reg add）、`findRunning` 在 Windows 上的端口探测
3. 国产 ROM 保活效果未知（小米/OPPO/vivo 可能几小时杀一次）

**合理的下一步**（按价值排序）：
1. 真机跑通并修 bug —— 优先级最高，其它都往后排
2. `ContentObserver` 实时监听相册，拍完几秒就传（替代 WorkManager 15 分钟周期）
3. 电脑端托盘图标（现在只能靠看板「退出」按钮关）
4. 上传断点续传（现在断网要整文件重传）
5. 正式签名 + 版本升级不丢配对信息

## 8. 代码约定

- 注释、日志、界面文案一律中文
- Go 侧不引第三方依赖，除二维码库；不要为了省几行代码引框架
- 手机端界面只用原生 View + Material，不引 Compose（体积和编译时间考虑）
- 所有用户可见报错要能指导操作，不写「错误 500」这种
- 改协议时**先改本文第 4 节**，再改两端代码

## 9. 给用户沟通时注意

- 用户非专业开发者，避免堆术语；报错要给具体点哪里
- 不要承诺「一定不会被杀后台」，这是 Android 生态的固有问题
- 用户在意「不丢数据」，涉及删除/覆盖的功能要先确认
