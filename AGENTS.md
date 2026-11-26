# LanSync · 项目交接说明（给 AI 助手）

> 这份文件是给接手这个项目的 AI 助手读的。用户是中文用户，Windows 环境，非专业开发者。
> 接手前请先读完本文，不要凭直觉改协议或重写架构。

## 1. 这个项目是什么

局域网内把手机照片/视频**自动**同步到电脑的私有工具，对标 Syncthing 但只做单向（手机 → 电脑）。

- **不做**：多设备互同步、双向、冲突合并、版本历史、公网穿透、账号体系
- **Docker/服务器？** 不需要。电脑端就是一个单文件 exe，双击跑起来就完事
- 电脑端 v1.3.1（新增「发给手机」+ 修复幽灵记录），手机端 v1.3.0（本次无需重装）

## 2. 技术栈与产物

| 端 | 语言/框架 | 产物 |
|---|---|---|
| 电脑端 | Go 1.21+，标准库 + `github.com/skip2/go-qrcode` | `dist/LanSync.exe`（5.3MB，`-H windowsgui` 无窗口） |
| 手机端 | Kotlin 1.9 / AGP 8.x，minSdk 26 / target 34 | `dist/LanSync.apk`（debug 签名，5.3MB） |
| 界面 | 单文件 HTML + 原生 JS，`go:embed` 内嵌进 exe | `server/web/index.html` |

## 3. 目录地图

```
server/
  main.go        服务入口、路由分发、鉴权、二维码、Windows 通知、开机自启、局域网 IP 探测
  store.go       落盘逻辑：路径净化、原子写入、SHA-256、index.json 索引
  outbox.go      发件箱：电脑→手机的文件投递（原地共享 / 拖拽副本 / 24h 过期）
  web/send.html  手机端收件页（内嵌，极简下载列表）
  dav.go         WebDAV 兜底通道（给 FolderSync / 资源管理器用）
  web/index.html 看板界面（内嵌，改完必须重新编译 exe）
app/app/src/main/
  java/com/lansync/app/
    Config.kt        SharedPreferences 存 host/port/token，baseUrl 拼装
    Api.kt           OkHttp 封装：check / put 流式上传
    MediaScanner.kt  扫 MediaStore 拿照片视频清单（按 RELATIVE_PATH 保留手机目录）
    SyncEngine.kt    核对 + 逐个上传的主流程（@Synchronized，三处调用共用）
    SyncWorker.kt    WorkManager 15 分钟周期任务（兜底，连网就跑）
    WatchService.kt  常驻守护：ContentObserver 盯媒体库，拍完照几秒内触发同步
    SyncService.kt   前台服务，手动「立即同步」用
    BootReceiver.kt  开机重挂定时任务 + 拉起守护服务
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
| `/api/pickdir` | POST | 是 | — | 弹系统文件夹选择框，返回 `{ok,dir}`；用户取消 `{ok:false,cancelled:true}`；不支持 `{ok:false,unsupported:true}` |
| `/api/sendlist` | GET | 是 | — | 发件箱清单 `{ok,items:[{id,name,size,missing,url}],count,url,qr,ttl}` |
| `/api/sendpick` | POST | 是 | — | 弹系统**多选文件**框，登记进发件箱；返回 `{ok,added,failed,count}` |
| `/api/sendput?name=<urlencoded>` | PUT | 是 | Body 为文件二进制 | 拖拽上传的副本，返回 `{ok,id,name,size}` |
| `/api/senddel` | POST | 是 | `{"id":"..."}` | 移出发件箱（副本连文件删，原文件不动） |
| `/api/sendclear` | POST | 是 | — | 清空发件箱 |
| `/s?t=<token>` | GET | 是 | — | 手机收件页（HTML），扫码链接自带令牌并种 Cookie |
| `/s/f?id=<itemId>` | GET | 是 | — | 下载文件，`http.ServeContent` 提供 Range/断点续传 |
| `/api/setdir` | POST | 是 | `{"dir":"D:\\手机备份","move":true}` | 切换接收目录，`move` 表示把旧文件一起搬过去；返回 `{ok,dir,old,moved,bytes,failed,indexed}` |
| `/qr.png` | GET | 否 | 可带 `?ip=` 覆盖地址，或 `?u=<完整链接>` 指定任意内容（仅放行 http/https） | PNG 二维码 |
| `/dav/*` | PROPFIND/MKCOL/PUT/GET/MOVE/DELETE/OPTIONS/LOCK | Basic | — | WebDAV 标准响应 |

**核对逻辑（核心，别改坏）**：`size` 相等 **且** `|mtime 差| <= 5 秒` 才算已存在，否则进 `need`。
路径一律先过 `sanitizeRel()` 再查索引，`..` 直接判为待传输（后续 PUT 会被 `absPath` 拦住）。

**手机端路径规则（v1.3 起）**：`MediaScanner` 用 `MediaStore.MediaColumns.RELATIVE_PATH` 拼出
`DCIM/Camera/IMG_0001.jpg` 这样的相对路径，电脑上照搬手机相册的目录结构。Android 9 及以下用 `DATA`
列截掉 `/storage/emulated/0/` 前缀；拿不到目录的文件放 `未分类/`。v1.2 是「日期/文件名」，两版不兼容。

**老路径认领（`handleCheck` 里的 `legacyDayPath` + `adoptPath`）**：升级到 v1.3 后同一张照片的目标路径
变了，光比路径会判「缺失」→ 整批重传几 GB。所以 `check` 会拿「大小 + mtime 完全相同」把 `YYYY-MM-DD/xxx`
形式的老记录**在磁盘上 rename 到新路径**，手机一个字节都不用传。三条硬约束：
① 只认 `^\d{4}-\d{2}-\d{2}/[^/]+$` 的老路径；② 该老路径不能出现在本次清单里（防互相顶替）；
③ 新位置已存在文件时不认领（`adoptPath` 先 `Stat`，绝不覆盖）。认领时把老记录的 `sha256` 一起搬走，
跨路径去重才不失灵。

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
| `store.go: getRoot/setRoot` | 接收目录一律走这两个函数，**不要直接读写 `rootDir`** | 上传协程与看板接口并发访问，切换目录时会出现「校验用旧目录、落盘用新目录」 |
| `store.go: rebuildIndex` | 切换目录后必须重建索引 | 旧记录留着，手机上报同路径同大小的文件会被判「已存在」而跳过，可新目录里根本没有 |
| `handleSetDir` | 先 `setRoot` 再 `migrateTo` | 迁移可能几分钟，先切目录能让这期间的上传直接落到新位置，不会丢在旧目录 |
| `handleSetDir` | 必须拒绝互相包含的目录（`isSubPath`） | 否则迁移和统计会递归进自己 |
| `pick_windows.go` | 结果走**临时 UTF-8 文件**而不是 stdout | PowerShell 默认按系统代码页输出，中文路径必乱码 |
| `pick_windows.go` | 脚本文件要带 BOM 写入；`exit 2` 专表示用户取消 | PS 5.1 读无 BOM 的 UTF-8 会当 ANSI，中文提示会乱；靠退出码区分取消与失败 |
| `pick_windows.go` | `FolderBrowserDialog` 必须挂一个不可见、`TopMost` 的 owner Form 再 `ShowDialog($owner)` | 脚本是隐藏窗口启动的，没有前台窗口，无主对话框会被系统排到浏览器后面，用户得回桌面才看得见 |
| `AndroidManifest.xml` | `CaptureActivity` 要 `tools:replace="android:screenOrientation"` 覆盖成 `portrait` | zxing 库自己声明的是 `fullSensor`，手机一歪扫码页就横过来 |
| `WatchService.kt` | 前台服务 + `startForeground(ID_WATCH, …)`，`START_STICKY`，`onDestroy` 必须注销 observer | 没有前台通知会被系统随时杀掉；observer 漏注册/漏注销会同时丢事件和漏内存 |
| `handleCheck` | 索引命中后**必须**再 `fileOnDisk()` 校验磁盘 | 用户在资源管理器里删文件不会更新 `index.json`，光看清单会一直回「已存在」，手机永远不补传（实测：删掉后仍回 `have:1`） |
| `healIndex` | 缺失比例 > 50% 且总数 > 20 时只告警不删 | 接收目录在没挂载的移动硬盘上时，一次误判会把整份清单清空 |
| `outbox.go` | 原地共享（`Copied=false`）的条目，过期/删除时**只能删记录，绝不能删文件** | 那是用户自己的文件，删了就出大事 |
| `outbox.go` | 下载必须走 `http.ServeContent`，别手写 `io.Copy` | 手写就没有 Range / Content-Length，手机进度条和断点续传全丢 |
| `SyncEngine.sync` | 必须 `@Synchronized` | 定时任务、守护服务、手动同步三处会同时开火，并发跑会把同一批文件传两遍 |
| 看板 HTML | 改完必须 `go build` 重新生成 exe | HTML 是 `go:embed` 编译进去的 |
| 看板 CSS | 带 `display:block` 的元素用 `hidden` 属性藏不住 | 类选择器优先级高于 UA 的 `[hidden]{display:none}`，要显式写 `xxx[hidden]{display:none}` |

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

**v1.3.0 改的七件事**：
1. 文件夹选择框加了 TopMost owner 窗体，不再被浏览器压在后面
2. 扫码页锁竖屏（覆盖 zxing 的 `fullSensor`）
3. 新增 `WatchService` 常驻守护：ContentObserver 收媒体库变更，攒 6 秒防抖后自动同步，拍完照十几秒电脑上就有
4. 接收目录改成照搬手机相册目录（`DCIM/Camera/...`），不再按日期分文件夹
5. `check` 支持把 v1.2 的日期老路径自动认领到新路径，升级不重传（Linux 侧实测：`have:1, need:[]`，文件已改名、索引已迁移、sha 保留）
6. 守护服务的通知是 `IMPORTANCE_MIN` + `setSilent`，不响不弹
7. 版本号 1.3.0（APK versionCode 4，可覆盖安装，配对信息不丢）

**v1.3.1 改的两件事**：
1. **修掉「电脑上删了照片，手机同步还说全部一致」** —— 根因是 `check` 只查内存索引不查磁盘，
   资源管理器删除会留下幽灵记录。现在命中索引后额外 `os.Stat` 校验（3000 条实测 14ms，可忽略），
   并在启动时后台 `healIndex` 清掉历史幽灵记录（实测日志：`启动自检：1 条记录对应的文件已不在磁盘上`）
2. **新增「发给手机」** —— 看板多一张卡片：系统多选文件框（原地共享、零拷贝）或拖拽（存副本），
   二维码指向 `/s` 收件页，手机浏览器点下载。实测 600MB 文件：上传/下载全程 RSS 稳定在 7MB，
   md5 一致，Range 返回 206，401/404 行为正确，24 小时过期后副本自动删除

**未验证（重要）**：
1. **APK 从未在真机跑过** — 沙箱无 Android 设备。扫码、MediaStore 读取、后台通知、热点场景下的自动同步都需要真机确认
2. **Windows 专有路径未在 Windows 上执行** — Toast 通知（PowerShell/WinRT）、开机自启（reg add）、`findRunning` 在 Windows 上的端口探测
3. 国产 ROM 保活效果未知（小米/OPPO/vivo 可能几小时杀一次）
4. **`/api/pickdir` 的系统选择框只能在 Windows 上验证** —— 沙箱是 Linux，只跑过「不支持」回退路径；PowerShell 脚本是照 WinForms 文档写的，真机第一次点「更改」要留意有没有弹框、有没有压到后面
5. **`/api/sendpick` 的多选文件框没在 Windows 上弹过** —— 和目录选择框同一套 PowerShell 写法，Linux 侧只测了 unsupported 回退
6. **手机端真机下载大文件**（尤其 iOS 的「文件」App）没验证，超过 4GB 的行为未知
7. **v1.3 三项手机端改动（竖屏、常驻守护、目录规则）都没上过真机** —— 编译通过不等于跑通，重点看：常驻通知是否出现、拍完照十几秒是否自动传、电脑上目录是否变成 `DCIM/Camera`

**合理的下一步**（按价值排序）：
1. 真机跑通并修 bug —— 优先级最高，其它都往后排
2. 电脑端托盘图标（现在只能靠看板「退出」按钮关）
3. 上传断点续传（现在断网要整文件重传）
4. 正式签名（现在 debug 签名，换签名会丢配对信息）
5. 通知里加「今天已备份 N 张」，让用户有确定感

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
