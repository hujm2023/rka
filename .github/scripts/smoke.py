"""Exercise the delivered container's public HTTP boundary."""
import http.cookiejar
import json
import os
import re
import urllib.error
import urllib.request

base = f"http://127.0.0.1:{os.environ.get('RKA_PORT', '18080')}"
client = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))


def request(path, payload=None):
    data = json.dumps(payload).encode() if payload is not None else None
    headers = {"Content-Type": "application/json"} if data else {}
    with client.open(urllib.request.Request(base + path, data=data, headers=headers), timeout=20) as response:
        return response.read()


assert json.loads(request("/readyz"))["status"] == "ready"
html = request("/").decode()
asset = re.search(r'src="(/assets/[^\"]+\.js)"', html).group(1)
assert len(request(asset)) > 1000
assert request("/knowledge").decode() == html
user = json.loads(request("/api/auth/login", {
    "email": os.environ["RKA_ADMIN_EMAIL"], "password": os.environ["RKA_ADMIN_PASSWORD"],
}))["user"]
assert user["role"] == "admin"
catalog = json.loads(request("/api/content/catalog"))
assert len(catalog["questions"]) > 1000 and len(catalog["materials"]) > 0
assert "answer" not in catalog["questions"][0]
try:
    request("/content/catalog.json")
    raise AssertionError("Private answer snapshot was served statically")
except urllib.error.HTTPError as error:
    assert error.code == 404
assert request("/content/licenses/practice-NOTICE.md")
print("Container smoke passed: readiness, assets, SPA, login, content and snapshot isolation")
