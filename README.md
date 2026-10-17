# PicG

以 Go、HTMX 與 templ 建置的目錄式相片瀏覽器。圖片來源以唯讀方式掛載，縮圖與設定會持久保存。

## Docker 部署

Compose 範例使用已存在的 `picg:latest` image，不會自行建置 image。第一次使用時，在專案根目錄建置：

```sh
docker build -t picg:latest .
```

預設相片目錄為專案下的 `./pic`。將相片放入該目錄後啟動：

```sh
docker compose -f docker-compose.example.yml up -d
```

開啟 <http://localhost:8080>。要停止服務：

```sh
docker compose -f docker-compose.example.yml down
```

Compose 會將 `./pic` 唯讀掛載到容器的 `/app/pic`，並以 Docker named volume `picg-data` 持久保存 `/app/app.sqlite`、`/app/thumbnails` 和 `/app/settings.json`。更新 image 時不要刪除這個 volume；`docker compose down -v` 會刪除持久化資料。

可以用環境變數指定其他相片目錄、主機連接埠或 image：

```sh
PICG_PHOTOS_PATH=/mnt/photos PICG_PORT=8090 docker compose -f docker-compose.example.yml up -d
```

PowerShell 範例：

```powershell
$env:PICG_PHOTOS_PATH = '/path-to-photo'
$env:PICG_PORT = '8090'
docker compose -f docker-compose.example.yml up -d
```

若使用 registry 上的 image，可透過 `PICG_IMAGE` 指定，例如：

```sh
PICG_IMAGE=registry.example.com/picg:1.0 docker compose -f docker-compose.example.yml up -d
```

