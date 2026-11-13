# 项目约定

## 本地运行

- 后端必须绑定 loopback：`BIND_ADDR=127.0.0.1`。
- Windows 上优先运行 `backend\start.bat`，它会 build 到 `backend\bin\server.exe` 并以 `127.0.0.1` 启动。
- 不要直接用 `go run ./cmd/server`。它会在 `%TEMP%\go-build...\server.exe` 生成临时二进制，路径每次变化，会反复触发 Windows 防火墙弹窗。
- 临时运行用：

```powershell
cd backend
go build -o bin\server.exe ./cmd/server
$env:BIND_ADDR="127.0.0.1"
.\bin\server.exe
```

- 前端保持 `vite.config.ts` 的 `host: "127.0.0.1"`，不要加 `--host 0.0.0.0`。
- 本地端口用 `8080`（后端）和 `5173`（前端）；不要监听 `0.0.0.0`。
