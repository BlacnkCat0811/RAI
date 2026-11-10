#!/usr/bin/env python3
"""Print the rai://connect link for a credential file made by `rai init` or
`rai admin new-dataset`. Open the link on the iPhone (AirDrop, Notes, Messages)
and confirm; the app stores the server and token and starts syncing.

The link contains the device token. Treat it like the credential file itself:
do not paste it into chats or tickets, and delete it from Notes after use.
"""
import json
import sys
import urllib.parse

if len(sys.argv) != 2:
    sys.exit("usage: connect-link.py CREDENTIAL_FILE")
with open(sys.argv[1], encoding="utf-8") as handle:
    package = json.load(handle)
enrollment = package.get("enrollment") or {}
base_url = package.get("baseURL", "")
epoch = enrollment.get("collectorEpoch", "")
token = enrollment.get("token", "")
if not (base_url.startswith("https://") and epoch and token):
    sys.exit("credential file has no https baseURL, collectorEpoch or token")
print("rai://connect?" + urllib.parse.urlencode({"url": base_url, "epoch": epoch, "token": token}))
