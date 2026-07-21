# OpenList WebDAV 元数据增强版

本分支在 OpenList 官方 `v4.2.3` 的基础上，为 WebDAV 增加由 OpenList 自己维护的时间、明文内容校验值和自定义属性元数据。它解决的是：上游网盘不保存客户端原始元数据，或者 `Crypt` 使上游哈希只能描述密文时，WebDAV 客户端可能误判文件状态并重复上传。

## 工作原理

文件内容仍然保存在原来的网盘或对象存储中，OpenList 不会修改上游网盘的协议能力。OpenList 会把逻辑路径、客户端时间、WebDAV 自定义属性以及用于校验文件身份的信息写入自己的数据库表。

客户端通过 WebDAV 完整上传文件时，OpenList 在内容进入存储驱动或 `Crypt` 之前，以流式方式同步计算一遍明文 `SHA-256`。这不需要再次读取或下载文件。该校验值既可作为 WebDAV 客户端可见的明文哈希，也用作统一的强 ETag。

后续通过 OpenList WebDAV 读取文件时：

1. OpenList 正常向上游查询文件。
2. OpenList 用路径、类型、大小，以及上游能够提供的对象 ID、哈希或后端修改时间校验元数据是否仍属于当前对象。
3. 校验通过后，WebDAV `PROPFIND`、`GET` 和 `HEAD` 使用数据库里的客户端时间、明文哈希和自定义属性。
4. `PROPFIND` 与 `GET`/`HEAD` 返回一致的 ETag；条件读取和条件 PUT 也依据同一套 ETag 判断。

上游网盘里显示的仍可能是上传时间；本功能提供的是 OpenList WebDAV 视角下的“虚拟原始时间”。

## 支持的客户端输入

| 客户端行为 | 处理结果 |
|---|---|
| `PUT` 带 `X-OC-Mtime` | 保存修改时间；没有独立创建时间时也将它作为创建时间 |
| `PUT` 带 `X-OC-Ctime` | 保存独立创建时间 |
| `PROPPATCH` 设置 `DAV:lastmodified` | 保存修改时间，兼容 rclone 的 ownCloud WebDAV 行为 |
| `PROPPATCH` 删除 `DAV:lastmodified` | 清除持久化修改时间 |
| `PROPPATCH` 设置或删除普通自定义属性 | 作为 WebDAV dead property 原样持久化，并在后续 `PROPFIND` 中返回 |
| rclone 同时提交 `oc:checksums` | 接受该属性以兼容 rclone，但不信任客户端声明；读取时返回 OpenList 自己计算的明文 `SHA-256` |
| `PUT` 不带时间头 | 上传文件并计算明文 `SHA-256`；清除旧修改时间，但保留已有创建时间和自定义属性 |
| `MKCOL` 带 `X-OC-Mtime` / `X-OC-Ctime` | 为目录保存修改时间和创建时间 |
| `PUT` 带 `If-Match` / `If-None-Match` | 根据 OpenList 的统一 ETag 和目标是否存在判断；不满足时返回 `412 Precondition Failed` |
| 非法时间值 | 忽略非法请求头；非法 `PROPPATCH` 属性返回属性级 `409 Conflict` |

`DAV:getlastmodified` 仍是只读的标准 WebDAV 属性，不能通过 `PROPPATCH` 直接修改。

## 文件操作联动

- `MOVE`：将源路径及其子路径的全部 WebDAV 元数据一起移动。
- `COPY`：复制源路径及其子路径的全部 WebDAV 元数据，并清空复制后通常会变化的后端身份绑定。
- `DELETE`：删除对应路径及其子路径的全部 WebDAV 元数据。
- `MKCOL`：创建目录成功后清除该路径可能残留的旧记录，再保存本次显式传入的目录时间。
- 覆盖上传：没有显式时间参数时不沿用旧文件的时间。

元数据操作与路径重写在 SQLite 事务中完成。

## 数据库与备份

新增表为 `webdav_metadata`（实际表名仍会遵循当前 OpenList 的 GORM 表前缀配置），通过 OpenList 启动时的 `AutoMigrate` 自动创建。它不修改现有文件、用户、存储或设置表。

这些虚拟时间、明文哈希和自定义属性只存在于 OpenList 数据库中，因此备份 OpenList 时必须同时备份完整数据目录，尤其是：

- `data.db`；
- `config.json`；
- 其他当前实例本来就需要保留的 OpenList 数据文件。

只备份上游网盘文件、丢失 `data.db`，会同时丢失虚拟时间、明文哈希和自定义属性。不要把生产数据库提交到 Git 仓库。

## 已验证行为

在独立数据目录和独立端口上完成了以下验证：

- 带 `X-OC-Mtime` 和 `X-OC-Ctime` 的 PUT；
- `PROPPATCH DAV:lastmodified`；
- MOVE 后保留时间；
- COPY 到另一目录后保留时间；
- 完整停止并重启 OpenList 后，MOVE/COPY 文件仍返回持久化时间；
- 不带时间的覆盖 PUT 会清除旧覆盖值；
- DELETE 后同名文件重新创建，不会继承旧记录；
- `PROPFIND Depth: 1` 中多个子文件分别返回自己的时间；
- 纳秒时间的解析、保存和包装对象读取；
- 明文 `SHA-256` 与 ETag 的一致性；
- `If-Match`、`If-None-Match` 和目标存在性判断；
- 自定义 WebDAV 属性的设置、删除、读取，以及混合 `PROPPATCH` 的原子性；
- rclone `oc:checksums` 与 `DAV:lastmodified` 同时提交时的兼容性；
- 后端对象身份改变后，旧明文哈希和虚拟元数据不会继续套用；
- `go test ./internal/db ./server/webdav`；
- `go test ./internal/net -run '^TestCheck(Read|Write)Preconditions$' -count=1`；
- `go vet ./internal/net ./internal/db ./server/common ./server/webdav`；
- Windows amd64 主程序编译。

另外使用实际的 `123Pan` 存储及其上层 `Crypt` 挂载完成了隔离验证：客户端 PUT 传入的修改时间为 Unix `946684800`、创建时间为 `915148800`，而 `/api/fs/list` 强制刷新后返回的上游物理时间为 `1784608458`。自定义 WebDAV 在两次完整进程重启后仍分别返回持久化的 2000/1999 年时间；再以 rclone 使用的 `PROPPATCH DAV:lastmodified` 格式写入 `1462518489` 后，第二次重启仍返回 2016 年时间，而上游物理时间保持为 2026 年。最后执行同目录 MOVE 改名，上游产生新的物理更新时间，但 WebDAV 的 2016 年修改时间随路径一同保留。

本轮元数据增强又在同一个真实 `123Pan` + `Crypt` 隔离实例上验证了完整流程：WebDAV PUT 得到的 ETag 与 OpenList 在加密前计算的明文 `SHA-256` 完全一致；带相同 `If-None-Match` 的 GET 返回 `304`；两次错误条件覆盖分别返回 `412`，使用正确 `If-Match` 覆盖返回 `204` 并生成新的明文 ETag；混合 PROPPATCH 同时保存纳秒修改时间和自定义属性，同时忽略客户端伪造的 ownCloud checksum；带纳秒时间的 MKCOL 正常保存目录时间。停止并重新启动整个测试进程后，文件 ETag、自定义属性和目录创建时间仍保持一致。

## 当前限制

1. 只有客户端明确发送时间时才能保存原始时间。测试中，rclone 的 ownCloud WebDAV 模式会发送 `X-OC-Mtime` 和 `PROPPATCH DAV:lastmodified`；当时测试的 Obsidian 与油猴备份没有发送原始时间，因此它们不会凭空得到本地原始时间。
2. 覆盖值目前只作用于 WebDAV 层。OpenList 管理页面和 `/api/fs/list` 仍可能显示上游时间。
3. 直接绕过 OpenList 访问上游网盘时，看不到这些虚拟时间、明文哈希和自定义属性。
4. 明文 `SHA-256` 只会在此增强版收到新的完整 WebDAV PUT 时生成；不会为了给历史文件补哈希而下载全部旧文件。历史文件在重新上传前仍使用原有的 ETag 回退逻辑。
5. 数据库可以保存纳秒时间，`DAV:creationdate` 也可返回亚秒精度；但标准 `DAV:getlastmodified` 使用 HTTP-date，本身只有秒精度。rclone 当前的 ownCloud `PROPPATCH` 也只提交 Unix 秒，因此不能从它未发送的数据中恢复亚秒部分。
6. 单个自定义属性限制为 `64 KiB`，单个资源的全部自定义属性序列化后限制为 `1 MiB`，避免客户端用属性撑大数据库。标准实时属性不能被自定义属性覆盖。
7. 为保证在 `Crypt` 加密前得到真实明文哈希，WebDAV PUT 强制让内容完整经过 OpenList，并额外计算一次流式 `SHA-256`。它不会二次读取文件，但会增加一遍哈希 CPU 开销，并且这条 WebDAV 上传路径不再使用可能绕过文件流的秒传行为。
8. 新上传以及 COPY/MOVE 后，后端物理修改时间会等待 5 分钟再绑定，避免 123Pan 等提供方从“客户端临时时间”异步切换为“真实上传时间”时误删刚保存的元数据。在这段稳定窗口内，如果有人绕过 OpenList 把文件替换为相同大小，且上游又没有稳定 ID/哈希，OpenList 可能暂时无法识别。
9. 条件 PUT 能防止正常 WebDAV 客户端基于旧 ETag 覆盖新版本，但无法让“OpenList 外部直接修改上游网盘”和本次请求形成跨系统原子事务。
10. 本阶段未实现 RFC 6578 增量同步令牌、跨重启持久锁和稳定资源 ID；需要这些能力的客户端仍可能回退为普通目录扫描。
11. OpenList `v4.2.3` 本身存在“在同一目录通过 WebDAV COPY 并改名会报 `copy in place`”的行为。另一次真实 `123Pan` + `Crypt` 测试中，跨目录 COPY 返回 `201` 后新对象在 20 秒内仍不可见。COPY 的最终行为取决于存储驱动及其缓存，不能仅根据 HTTP 成功状态判断；这不是本增强功能引入的问题。
12. 物理文件操作成功后，如果元数据数据库操作恰好失败，请求可能返回 `500`，但上游文件操作已经发生。遇到这种情况应先检查实际文件状态，再重试或修复数据库，避免盲目重复操作。

## 分支和版本策略

- `main`：尽量保持为 GitHub fork 的官方跟踪分支，不放个人功能。
- `webdav-mtime`：本功能的长期维护分支。
- `custom-vX.Y.Z.N`：验证通过后给自定义版本打的稳定标签，例如 `custom-v4.2.3.1`。

功能被拆成小提交，便于判断升级冲突来自数据库、WebDAV 写入、PROPPATCH，还是文件生命周期联动。

## 合并官方更新

下面采用 merge，不改写已经推送的历史，对 Git 初学者更安全。

```powershell
git switch webdav-mtime
git status --short --branch
git fetch upstream --tags
git merge <新的官方版本标签>
```

例如将来升级到官方 `v4.2.4`：

```powershell
git merge v4.2.4
```

如果没有冲突，Git 会直接得到“官方新版 + 自定义功能”。如果有冲突，只处理 Git 标出的文件，不要用 `git reset --hard` 或删除目录重新来过。处理后依次运行：

```powershell
go test ./internal/db ./server/webdav
go test ./internal/net -run '^TestCheck(Read|Write)Preconditions$' -count=1
go vet ./internal/net ./internal/db ./server/common ./server/webdav
go build -tags=jsoniter .
git push origin webdav-mtime
```

随后还应重复真实 WebDAV 的 PUT、PROPPATCH、重启后 PROPFIND、MOVE/COPY/DELETE 测试。确认数据库备份和运行结果都正常后，再创建新的 `custom-vX.Y.Z.N` 标签。

## 部署与回滚原则

1. 第一次让自定义程序读取现有实例前，停止 OpenList 并备份完整数据目录。
2. 先用数据目录副本或测试实例验证存储能加载、WebDAV 能读写、时间能跨重启保持。
3. 再替换生产程序；不要同时修改存储配置和数据库位置。
4. 需要回滚时，优先恢复“旧程序 + 升级前的数据目录备份”这一整套组合。

官方程序通常会忽略新增的独立表，但这不代表任何未来版本的数据库迁移都可以无备份降级。可靠回滚依赖升级前备份，而不是依赖数据库碰巧兼容。
