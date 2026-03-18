# TODO: eBay Bot Setup

Once you have your eBay API keys, follow these steps to fully enable the eBay deal monitoring features.

---

## 1. Obtain eBay API Credentials

1. Go to [developer.ebay.com](https://developer.ebay.com/) and sign in (or create a developer account).
2. Navigate to **My Account > Application Keys**.
3. Create a new **Production** keyset (not Sandbox).
   - You need the **App ID (Client ID)** and **Cert ID (Client Secret)** from the Production keyset.
   - The Browse API uses the **Client Credentials** grant type (no user auth required).
4. Make sure your app has access to the **Buy > Browse API**:
   - Go to **My Account > Application Access** in the developer portal.
   - Under your production keyset, ensure "Buy - Browse API" is listed/enabled.
   - If it's not available, you may need to request access or subscribe to the API from the eBay developer marketplace.

---

## 2. Update Local Secrets

1. Open `.env` in the project root.
2. Replace the placeholder values:
   ```
   EBAY_CLIENT_ID=your-actual-production-client-id
   EBAY_CLIENT_SECRET=your-actual-production-cert-id
   ```
3. Verify locally by running the server — you should see `"eBay OAuth token refreshed"` in logs instead of the `"eBay API credentials not configured"` warning.

---

## 3. Push Secrets to GitHub

Run the existing secret sync script to push the updated `.env` values to GitHub Actions:

```powershell
.\sync_secrets.ps1
```

This will update `EBAY_CLIENT_ID` and `EBAY_CLIENT_SECRET` in your GitHub repository secrets, which are used by the deploy workflow (`.github/workflows/deploy.yml`).

---

## 4. Deploy to Cloud Run

Push to `main` (or trigger the workflow manually) to deploy the updated code with real eBay credentials:

```bash
git push origin main
```

The deploy workflow will pass `EBAY_CLIENT_ID` and `EBAY_CLIENT_SECRET` as environment variables to Cloud Run. The bot will detect real credentials and enable eBay features automatically.

---

## 5. Create Cloud Scheduler Job for eBay

Once the deployment is confirmed working, create a Cloud Scheduler job to trigger eBay processing on a regular interval. The `/process-ebay` endpoint is already built and ready.

```bash
gcloud scheduler jobs create http rfd-discord-bot-ebay \
  --location=us-central1 \
  --schedule="*/15 6-23 * * *" \
  --uri="https://YOUR_CLOUD_RUN_URL/process-ebay" \
  --http-method=POST \
  --oidc-service-account-email=YOUR_SERVICE_ACCOUNT@YOUR_PROJECT.iam.gserviceaccount.com \
  --oidc-audience="https://YOUR_CLOUD_RUN_URL"
```

**Replace:**
- `YOUR_CLOUD_RUN_URL` — your Cloud Run service URL (find it in the GCP Console under Cloud Run > rfd-discord-bot)
- `YOUR_SERVICE_ACCOUNT` / `YOUR_PROJECT` — the service account email used for Cloud Scheduler (same one used for the RFD scheduler job)

**Schedule breakdown:** `*/15 6-23 * * *` = every 15 minutes from 6 AM to 11 PM daily. Adjust as needed:
- Every 10 minutes: `*/10 * * * *`
- Every 30 minutes during business hours: `*/30 9-17 * * *`
- Every hour: `0 * * * *`

**Note:** The eBay Browse API has a daily call limit of 5,000 calls/day for the basic tier. Each poll uses ~1 API call per 25 sellers (you have 21, so 1 call). At 15-minute intervals over 18 hours, that's ~72 calls/day — well within limits.

---

## 6. Verify eBay Pipeline is Working

After the scheduler is created, monitor the logs for the first few runs:

```bash
gcloud logging read "resource.type=cloud_run_revision AND resource.labels.service_name=rfd-discord-bot AND textPayload:ebay" --limit=50 --format="table(timestamp, textPayload)"
```

**What to look for:**
- `"Loaded active eBay sellers"` — Firestore sellers were loaded (or seeded on first run)
- `"Fetched eBay listings"` — Browse API call succeeded
- `"New eBay items to analyze"` — new items found since last poll
- `"Tier-1 screening results"` — batch AI screening ran
- `"eBay deal passed both tiers"` — a deal made it through both AI tiers
- `"Persisted warm/hot eBay items"` — deals saved to Firestore

**Common issues:**
- `"eBay client not configured"` — credentials are still placeholder/empty
- `"eBay token request failed: HTTP 401"` — wrong Client ID or Client Secret
- `"eBay Browse API error: HTTP 403"` — Browse API access not enabled for your app
- `"all model tiers exhausted"` — Gemini API quota exceeded, check your Gemini billing

---

## 7. Register Updated Discord Commands

The slash command choices have been updated with new deal types (rfd_*, ebay_*, warm_hot_all, hot_all). You need to re-register commands so Discord picks up the new options:

```bash
go run cmd/register-commands/main.go
```

Or if running from the deployed environment, the commands should auto-register on startup if that logic is in place. Otherwise, run locally with the bot token set in `.env`.

**New deal type choices users will see:**
| Type | Description |
|------|-------------|
| `rfd_all` | All RFD deals |
| `rfd_tech` | RFD tech deals only |
| `rfd_warm_hot` | RFD warm + hot deals |
| `rfd_warm_hot_tech` | RFD warm + hot tech deals |
| `rfd_hot` | RFD hot deals only |
| `rfd_hot_tech` | RFD hot tech deals only |
| `ebay_warm_hot` | eBay warm + hot deals |
| `ebay_hot` | eBay hot deals only |
| `warm_hot_all` | Warm + hot from ALL sources (RFD + eBay) |
| `hot_all` | Hot from ALL sources (RFD + eBay) |

---

## 8. Update Existing Subscriptions (If Any)

**Important:** The old deal type names (`all`, `tech`, `hot`, `warm_hot`, etc.) are no longer recognized. Any existing subscriptions in Firestore using old type names will stop matching deals.

To fix this, manually update documents in the `subscriptions` Firestore collection:
- `"all"` → `"rfd_all"`
- `"tech"` → `"rfd_tech"`
- `"warm_hot"` → `"rfd_warm_hot"`
- `"warm_hot_tech"` → `"rfd_warm_hot_tech"`
- `"hot"` → `"rfd_hot"`
- `"hot_tech"` → `"rfd_hot_tech"`

You can do this via the [Firebase Console](https://console.firebase.google.com/) > Firestore > `subscriptions` collection, or write a quick migration script.

---

## 9. Manage eBay Sellers (Optional)

The default 21 sellers are seeded into Firestore automatically on first run (if the `ebay_sellers` collection is empty). After that, you manage sellers through Firestore directly:

- **Add a seller:** Create a new document in `ebay_sellers` with `username`, `active: true`, and optionally `store_name`.
- **Disable a seller:** Set `active: false` on their document (the bot only queries active sellers).
- **Remove a seller:** Delete their document from the collection.

The bot will pick up changes on the next poll cycle — no redeployment needed.

---

## Summary Checklist

- [ ] Get eBay Production API keys (Client ID + Client Secret)
- [ ] Update `.env` with real keys
- [ ] Run `sync_secrets.ps1` to push to GitHub
- [ ] Deploy to Cloud Run (push to main)
- [ ] Create Cloud Scheduler job for `/process-ebay`
- [ ] Verify logs show successful eBay polling
- [ ] Re-register Discord slash commands
- [ ] Migrate any existing subscriptions to new type names
