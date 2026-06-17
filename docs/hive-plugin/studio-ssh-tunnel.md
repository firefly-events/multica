# Studio Multica SSH tunnel

Use this from your **local Mac** when you want both Multica instances available at the same time:

- **Local Multica** stays on `http://localhost:3000`
- **Studio Multica** is forwarded to `http://localhost:3300`
- **Studio backend API** is forwarded to `http://localhost:38080`
- **Studio Hermes dashboard** is forwarded to `http://localhost:39119`

## One-shot tunnel

Run this on the local machine:

```sh
ssh -N \
  -L 3300:127.0.0.1:3000 \
  -L 38080:127.0.0.1:8080 \
  -L 39119:127.0.0.1:9119 \
  hive@192.168.86.91
```

Then open:

- Studio Multica web: `http://localhost:3300`
- Studio Multica API: `http://localhost:38080`
- Studio Hermes dashboard: `http://localhost:39119`

## Reusable SSH config entry

Add this to the local machine's `~/.ssh/config`:

```sshconfig
Host studio-multica
  HostName 192.168.86.91
  User hive
  ServerAliveInterval 30
  ServerAliveCountMax 3
  ExitOnForwardFailure yes
  LocalForward 3300 127.0.0.1:3000
  LocalForward 38080 127.0.0.1:8080
  LocalForward 39119 127.0.0.1:9119
```

Then connect with:

```sh
ssh -N studio-multica
```

## Notes

- Keep the Studio tunnel on `3300` so the local Multica instance can keep using `3000`.
- The Studio-side services are expected on:
  - web `127.0.0.1:3000`
  - backend `127.0.0.1:8080`
  - Hermes dashboard `127.0.0.1:9119`
- If you only need Multica web + API, you can omit the `39119` forward.
