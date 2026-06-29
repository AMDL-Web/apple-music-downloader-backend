# wrapper

A tool to decrypt Apple Music songs. An active subscription is still needed.

Supports only x86_64 and arm64 Linux.

## Features

- **Concurrent Decryption**: Multi-threaded architecture allows simultaneous handling of multiple decryption requests
- **Three Service Ports**: Decrypt (10020), M3U8 (20020), Account API (30020)
- **High Performance**: Each connection is processed in a separate thread for maximum throughput
- **API-Driven Login**: Login via HTTP API with SSE status updates

## Installation

Installation methods:

- [Docker](#docker) (recommended)
- Prebuilt binaries (from [releases](https://github.com/WorldObservationLog/wrapper/releases) or [actions](https://github.com/WorldObservationLog/wrapper/actions))
- [Build from source](#build-from-source)

### Docker

Available for x86_64 and arm64. Need to download prebuilt version from releases or actions.

1. Build image:

```
docker build --tag wrapper .
```

2. Run:

```
docker run -v ./rootfs/data:/app/rootfs/data -p 10020:10020 -p 20020:20020 -p 30020:30020 -e args="-H 0.0.0.0" wrapper
```

3. Login via API (see [Account API](#account-api) section)

### Build from source

1. Install dependencies:

- Build tools:

  ```
  sudo apt install build-essential cmake wget unzip git
  ```

- LLVM:

  ```
  sudo bash -c "$(wget -O - https://apt.llvm.org/llvm.sh)"
  ```

- Android NDK r23b:
  ```
  wget -O android-ndk-r23b-linux.zip https://dl.google.com/android/repository/android-ndk-r23b-linux.zip
  unzip -q -d ~ android-ndk-r23b-linux.zip
  ```

2. Build:

```
git clone https://github.com/WorldObservationLog/wrapper
cd wrapper
mkdir build
cd build
cmake ..
make -j$(nproc)
```

## Usage

```
Usage: wrapper [OPTION]...

  -h, --help              Print help and exit
  -V, --version           Print version and exit
  -H, --host=STRING         (default=`127.0.0.1')
  -D, --decrypt-port=INT    (default=`10020')
  -M, --m3u8-port=INT       (default=`20020')
  -A, --account-port=INT    (default=`30020')
  -P, --proxy=STRING        (default=`')
```

## Account API

The Account API (port 30020) provides login management via HTTP endpoints.

### Endpoints

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/info` | GET | Get current login status and account info |
| `/login` | POST | Submit credentials to start login |
| `/2fa` | POST | Submit 2FA verification code |
| `/logout` | POST | Clear login state and saved credentials |
| `/events` | GET | SSE stream for real-time status updates |

### Login Flow

```bash
# 1. Check status
curl http://localhost:30020/info
# Returns: {"logged_in":false,"status":"need_login"}

# 2. Subscribe to SSE events (in another terminal)
curl -N http://localhost:30020/events

# 3. Login
curl -X POST http://localhost:30020/login \
  -H "Content-Type: application/json" \
  -d '{"username":"your@email.com","password":"your_password"}'

# 4. If 2FA required, submit code
curl -X POST http://localhost:30020/2fa \
  -H "Content-Type: application/json" \
  -d '{"code":"123456"}'

# 5. Check account info
curl http://localhost:30020/info
# Returns: {"logged_in":true,"storefront_id":"...","dev_token":"...","music_token":"..."}

# 6. Logout (optional)
curl -X POST http://localhost:30020/logout
# Returns: {"message":"logged out"}
```

### SSE Events

The `/events` endpoint streams status updates:
- `{"status":"need_login"}` - Waiting for login
- `{"status":"logging_in"}` - Login in progress
- `{"status":"need_2fa"}` - 2FA code required
- `{"status":"logged_in"}` - Successfully logged in
- `{"status":"login_failed","error":"..."}` - Login failed

## Special thanks

- Anonymous, for providing the original version of this project and the legacy Frida decryption method.
- chocomint, for providing support for arm64 arch.
