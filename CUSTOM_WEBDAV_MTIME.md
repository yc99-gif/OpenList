# OpenList WebDAV 文件时间持久化版

本分支在 OpenList 官方 `v4.2.3` 的基础上，为 WebDAV 增加由 OpenList 自己维护的文件时间元数据。它解决的是：上游网盘不保存客户端原始时间时，WebDAV 客户端刷新目录后只能看到网盘的上传时间，进而可能误判所有文件都需要重新上传。

## 工作原理

文件内容仍然保存在原来的网盘或对象存储中，OpenList 不会修改上游网盘的协议能力。客户端明确传入文件时间时，OpenList 会把逻辑路径、修改时间、创建时间以及用于校验文件身份的信息写入自己的数据库表。

后续通过 OpenList WebDAV 读取文件时：

1. OpenList 正常向上游查询文件。
2. OpenList 用路径、类型、大小，以及上游能够提供的对象 ID 或哈希校验元数据是否仍属于当前文件。
3. 校验通过后，WebDAV `PROPFIND`、`GET` 和 `HEAD` 使用数据库里的客户端时间。
4. ETag 也基于覆盖后的时间生成，避免同步软件因 ETag 和时间不一致而反复更新。

上游网盘里显示的仍可能是上传时间；本功能提供的是 OpenList WebDAV 视角下的“虚拟原始时间”。

## 支持的客户端输入

| 客户端行为 | 处理结果 |
|---|---|
| `PUT` 带 `X-OC-Mtime` | 保存修改时间；没有独立创建时间时也将它作为创建时间 |
| `PUT` 带 `X-OC-Ctime` | 保存独立创建时间 |
| `PROPPATCH` 设置 `DAV:lastmodified` | 保存修改时间，兼容 rclone 的 ownCloud WebDAV 行为 |
| `PROPPATCH` 删除 `DAV:lastmodified` | 清除持久化修改时间 |
| `PUT` 不带时间头 | 上传文件，但清除该路径可能残留的旧时间覆盖值 |
| 非法时间值 | 忽略非法请求头；非法 `PROPPATCH` 属性返回属性级 `409 Conflict` |

`DAV:getlastmodified` 仍是只读的标准 WebDAV 属性，不能通过 `PROPPATCH` 直接修改。

## 文件操作联动

- `MOVE`：将源路径及其子路径的时间元数据一起移动。
- `COPY`：复制源路径及其子路径的时间元数据，并清空复制后通常会变化的对象 ID。
- `DELETE`：删除对应路径及其子路径的时间元数据。
- `MKCOL`：创建目录成功后清除该路径可能残留的旧记录。
- 覆盖上传：没有显式时间参数时不沿用旧文件的时间。

元数据操作与路径重写在 SQLite 事务中完成。

## 数据库与备份

新增表为 `webdav_metadata`（实际表名仍会遵循当前 OpenList 的 GORM 表前缀配置），通过 OpenList 启动时的 `AutoMigrate` 自动创建。它不修改现有文件、用户、存储或设置表。

这些时间只存在于 OpenList 数据库中，因此备份 OpenList 时必须同时备份完整数据目录，尤其是：

- `data.db`；
- `config.json`；
- 其他当前实例本来就需要保留的 OpenList 数据文件。

只备份上游网盘文件、丢失 `data.db`，会同时丢失虚拟时间。不要把生产数据库提交到 Git 仓库。

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
- `go test ./internal/db ./server/webdav`；
- Windows amd64 主程序编译。

## 当前限制

1. 只有客户端明确发送时间时才能保存时间。测试中，rclone 的 ownCloud WebDAV 模式会发送 `X-OC-Mtime` 和 `PROPPATCH DAV:lastmodified`；当时测试的 Obsidian 与油猴备份没有发送原始时间，因此它们不会凭空得到本地原始时间。
2. 覆盖值目前只作用于 WebDAV 层。OpenList 管理页面和 `/api/fs/list` 仍可能显示上游时间。
3. 直接绕过 OpenList 访问上游网盘时，看不到这些虚拟时间。
4. 校验优先使用大小、对象 ID 和上游已有哈希，不会为了判断文件身份而下载整个文件计算哈希。代价是：某些上游既没有稳定 ID/哈希，又在 OpenList 之外把文件替换为相同大小时，旧记录可能无法立即识别为过期。
5. 时间以 Unix 秒保存；客户端提供的亚秒精度不会保留。
6. OpenList `v4.2.3` 本身存在“在同一目录通过 WebDAV COPY 并改名会报 `copy in place`”的行为。复制到另一目录可以正常完成；这不是时间持久化代码引入的问题。
7. 物理文件操作成功后，如果元数据数据库操作恰好失败，请求可能返回 `500`，但上游文件操作已经发生。遇到这种情况应先检查实际文件状态，再重试或修复数据库，避免盲目重复操作。

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
