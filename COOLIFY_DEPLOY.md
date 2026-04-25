# Coolify Source Deploy

Deploy mode yang dipakai: `Nixpacks / Source`, bukan `Dockerfile`.

## Setting di Coolify

- Build pack: `Nixpacks`
- Port: `3000`
- Base directory: root project ini
- Health check path: `/health`

## Command

Command sudah disiapkan di [nixpacks.toml](/d:/AZKAZAMDIGITAL%205/Golang%20WA%20Instablast/nixpacks.toml):

- install: `go mod download`
- build: `go build -o wa-gateway .`
- start: `./wa-gateway`

## Environment Variable

Set minimal ini di Coolify:

```env
APP_HOST=0.0.0.0
APP_PORT=3000
APP_DEBUG=false
DB_URI=file:storages/whatsapp.db?_pragma=foreign_keys(1)&_pragma=busy_timeout(15000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=temp_store(MEMORY)
NVIDIA_API_KEY=
```

Catatan:

- `APP_PORT` juga bisa mengikuti env `PORT` dari platform.
- Storage aplikasi berada di folder `storages/`, jadi di Coolify sebaiknya mount persistent volume ke path `/app/storages`.
- Admin default akan di-seed otomatis:
  - email: `azam@gmail.com`
  - password: `Nr201105`
