"""Minimal App Store Connect API client.

    ASC_KEY_ID=... ASC_ISSUER_ID=... ASC_KEY_PATH=AuthKey_....p8 \
        python3 asc.py GET /v1/apps
    python3 asc.py POST /v1/betaGroups '{"data": ...}'
    python3 asc.py profile "urdp tvOS App Store" out.mobileprovision

Prints the HTTP status, then the JSON body. Needs PyJWT and cryptography.
"""
import base64
import json
import os
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

import jwt


def token():
    now = int(time.time())
    with open(os.environ["ASC_KEY_PATH"]) as f:
        key = f.read()
    return jwt.encode(
        {"iss": os.environ["ASC_ISSUER_ID"], "iat": now, "exp": now + 1100, "aud": "appstoreconnect-v1"},
        key,
        algorithm="ES256",
        headers={"kid": os.environ["ASC_KEY_ID"], "typ": "JWT"},
    )


def call(method, path, body=None):
    url = path if path.startswith("http") else "https://api.appstoreconnect.apple.com" + path
    req = urllib.request.Request(
        url,
        method=method,
        data=json.dumps(body).encode() if body is not None else None,
        headers={"Authorization": "Bearer " + token(), "Content-Type": "application/json"},
    )
    try:
        with urllib.request.urlopen(req) as r:
            data = r.read()
            return r.status, json.loads(data) if data else None
    except urllib.error.HTTPError as e:
        data = e.read()
        return e.code, json.loads(data) if data else None


def main():
    if sys.argv[1] == "profile":
        name, out = sys.argv[2], sys.argv[3]
        status, d = call("GET", "/v1/profiles?filter[name]=" + urllib.parse.quote(name))
        profiles = [p for p in (d or {}).get("data", []) if p["attributes"]["profileState"] == "ACTIVE"]
        if status != 200 or not profiles:
            sys.exit(f"no active profile named {name!r} ({status})")
        with open(out, "wb") as f:
            f.write(base64.b64decode(profiles[0]["attributes"]["profileContent"]))
        print(profiles[0]["attributes"]["uuid"])
        return
    method, path = sys.argv[1], sys.argv[2]
    body = json.loads(sys.argv[3]) if len(sys.argv) > 3 else None
    status, d = call(method, path, body)
    print(status)
    print(json.dumps(d, indent=1))
    if status >= 400:
        sys.exit(1)


if __name__ == "__main__":
    main()
