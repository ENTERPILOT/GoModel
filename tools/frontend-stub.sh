#!/usr/bin/env sh
# Writes a placeholder dashboard bundle into internal/admin/dashboard/static/dist
# so the Go test suites, which //go:embed that directory, compile and pass
# without Node. The three tests that read the bundle only check that
# index.html references hashed assets that resolve, so this is enough.
#
# It never replaces a real build, and it is never what ships: `make build`,
# the Docker image, and the release workflow all build the real bundle
# (docs/adr/0010-dashboard-built-in-ci.md).
set -eu

dist=internal/admin/dashboard/static/dist
if [ -f "$dist/index.html" ]; then
	echo "frontend-stub: $dist/index.html exists; leaving the dashboard bundle alone"
	exit 0
fi

mkdir -p "$dist/assets" "$dist/fonts"
printf '/* dashboard stub: run make frontend for the real bundle */\n' > "$dist/assets/index-stub.css"
printf '// dashboard stub: run make frontend for the real bundle\n' > "$dist/assets/index-stub.js"
printf '/* dashboard stub */\n' > "$dist/fonts/inter.css"
printf '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16"><rect width="16" height="16" fill="#1e6b5a"/></svg>\n' > "$dist/favicon.svg"
cat > "$dist/index.html" <<'HTML'
<!doctype html>
<html lang="en">
  <head>
    <meta charset="UTF-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1.0, viewport-fit=cover" />
    <meta name="robots" content="noindex, nofollow, nosnippet, noimageindex" />
    <title>GoModel Dashboard</title>
    <link rel="icon" type="image/svg+xml" href="/admin/static/favicon.svg" />
    <link rel="stylesheet" href="/admin/static/fonts/inter.css" />
    <script type="module" crossorigin src="/admin/static/assets/index-stub.js"></script>
    <link rel="stylesheet" crossorigin href="/admin/static/assets/index-stub.css">
  </head>
  <body>
    <!-- Dashboard stub written by tools/frontend-stub.sh for test builds. -->
    <p>This binary embeds a dashboard stub. Run <code>make frontend</code> and rebuild for the real dashboard.</p>
    <div id="app" class="app"></div>
  </body>
</html>
HTML
echo "frontend-stub: wrote a placeholder dashboard bundle to $dist"
