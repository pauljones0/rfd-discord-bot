#!/usr/bin/env python3
"""Deploy a prebuilt image; never register commands or activate a scheduler."""
import argparse
import json
import re
import subprocess
import time
import urllib.request
import urllib.parse
from pathlib import Path


def run(*args, json_output=False, allow_missing=False):
    result = subprocess.run(["gcloud", "--quiet", *args], capture_output=True, text=True)
    if result.returncode:
        if allow_missing and "NOT_FOUND" in result.stderr:
            return None
        raise RuntimeError(result.stderr)
    return json.loads(result.stdout) if json_output else result.stdout.strip()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--project", required=True)
    parser.add_argument("--image", required=True, help="Immutable Artifact Registry digest")
    parser.add_argument("--service", required=True)
    parser.add_argument("--config", type=Path, required=True, help="Private JSON environment mapping")
    parser.add_argument("--region", default="us-central1")
    args = parser.parse_args()
    config = json.loads(args.config.read_text())
    assert args.region == "us-central1", "Current zero-cost profile is validated in Iowa only"
    assert re.fullmatch(r"[a-z][a-z0-9-]{2,48}", args.service)
    assert "@sha256:" in args.image, "Deploy an immutable digest"
    assert args.image.startswith(f"{args.region}-docker.pkg.dev/{args.project}/"), "Co-locate registry and runtime"
    assert config["GCP_PROJECT"] == args.project
    assert config["BOT_DEPLOYMENT_MODE"] in ("fixture", "production")
    assert config["FIRESTORE_NAMESPACE"].endswith("-" + config["BOT_DEPLOYMENT_MODE"])
    assert config["BOT_REQUEST_ROLE"] in ("worker", "commands")
    if config["BOT_REQUEST_ROLE"] == "worker":
        assert config["SUBSCRIPTIONS_NAMESPACE"] != config["FIRESTORE_NAMESPACE"]
        assert config["SUBSCRIPTIONS_NAMESPACE"].endswith("-" + config["BOT_DEPLOYMENT_MODE"])
    assert len(config["SCHEDULER_TOKEN"]) >= 32
    validate_gemini(config)
    billing = run("billing", "projects", "describe", args.project, "--format=json", json_output=True)
    assert billing["billingEnabled"], "An active billing account is required for the ongoing free tier"
    databases = run("firestore", "databases", "list", f"--project={args.project}", "--format=json", json_output=True)
    assert any(d["name"].endswith("/(default)") and d.get("freeTier") and d["locationId"] == args.region and d["type"] == "FIRESTORE_NATIVE" for d in databases), "Requires the existing eligible regional Native database"
    sa_id = args.service + "-runtime"
    sa = f"{sa_id}@{args.project}.iam.gserviceaccount.com"
    existing = run("iam", "service-accounts", "describe", sa, f"--project={args.project}", "--format=json", json_output=True, allow_missing=True)
    if existing is None:
        run("iam", "service-accounts", "create", sa_id, f"--project={args.project}", f"--display-name={args.service} Firestore runtime")
    # New service-account identities can take a few seconds to reach IAM.
    for attempt in range(6):
        try:
            run("projects", "add-iam-policy-binding", args.project, f"--member=serviceAccount:{sa}", "--role=roles/datastore.user", "--condition=None")
            break
        except RuntimeError as error:
            if "does not exist" not in str(error) or attempt == 5:
                raise
            time.sleep(min(2 ** attempt, 20))
    run("run", "deploy", args.service, f"--project={args.project}", f"--region={args.region}", f"--image={args.image}", f"--service-account={sa}",
        f"--env-vars-file={args.config.resolve()}", "--port=8080", "--cpu=0.08", "--memory=256Mi", "--execution-environment=gen1", "--concurrency=1", "--min=0", "--max=1", "--max-instances=1", "--cpu-throttling", "--no-cpu-boost", "--timeout=1800", "--allow-unauthenticated" if config["BOT_REQUEST_ROLE"] == "commands" else "--no-allow-unauthenticated")
    if config["BOT_REQUEST_ROLE"] == "worker":
        run("run", "services", "add-iam-policy-binding", args.service, f"--project={args.project}", f"--region={args.region}", f"--member=serviceAccount:{sa}", "--role=roles/run.invoker")
    run("artifacts", "docker", "tags", "add", args.image, args.image.split("@", 1)[0] + ":production-" + args.service)
    # JSON contains only public endpoints/identity metadata, never environment.
    url = run("run", "services", "describe", args.service, f"--project={args.project}", f"--region={args.region}", "--format=value(status.url)")
    print(json.dumps({"service": args.service, "region": args.region, "url": url, "mode": config["BOT_DEPLOYMENT_MODE"], "scheduler_activated": False}))


def validate_gemini(config, billing_loader=None, key_loader=None):
    """The cloud profile accepts only a dedicated key in an unbilled project."""
    key = config.get("GEMINI_API_KEY", "")
    if not key:
        return
    assert config["BOT_REQUEST_ROLE"] == "worker", "Commands must not have AI credentials"
    assert "," not in key, "Only one dedicated free Gemini key is allowed"
    assert config.get("GEMINI_MODELS") == "gemini-3.5-flash-lite"
    project = config.get("GEMINI_FREE_PROJECT", "")
    number = config.get("GEMINI_FREE_PROJECT_NUMBER", "")
    assert re.fullmatch(r"rfd-gemini-free-[a-z0-9-]+", project)
    assert re.fullmatch(r"[0-9]+", number)
    billing_loader = billing_loader or (lambda p: run("billing", "projects", "describe", p, "--format=json", json_output=True))
    billing = billing_loader(project)
    assert billing.get("projectId") == project and billing.get("billingEnabled") is False and billing.get("billingAccountName") == "", "Gemini project must have no billing account linked"
    if key_loader is None:
        token = run("auth", "print-access-token")
        def key_loader(secret):
            url = "https://apikeys.googleapis.com/v2/keys:lookupKey?" + urllib.parse.urlencode({"keyString": secret})
            try:
                with urllib.request.urlopen(urllib.request.Request(url, headers={"Authorization": "Bearer " + token, "x-goog-user-project": project}), timeout=10) as response:
                    return json.load(response)
            except Exception:
                raise RuntimeError("Could not verify free Gemini key ownership") from None
    lookup = key_loader(key)
    parent = f"projects/{number}/locations/global"
    name = lookup.get("name", "")
    prefix = parent + "/keys/"
    assert lookup.get("parent") == parent and name.startswith(prefix) and name[len(prefix):] and "/" not in name[len(prefix):], "Gemini key must belong to the unbilled project"


if __name__ == "__main__":
    main()
