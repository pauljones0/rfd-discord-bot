# RFD Hot Deals Discord Bot

A brief overview of the bot's purpose (scrapes RFD Hot Deals RSS, posts new deals to Discord via webhook) and its Google Cloud Run architecture.

## Features

*   Monitors RFD Hot Deals RSS feed.
*   Identifies new deals using Firestore for state management.
*   Extracts deal title, RFD link, author, published date, and attempts to find a direct item link.
*   Filters out "Sponsored" posts.
*   Includes a stub for future province-based filtering.
*   Formats deals as rich Discord embeds.
*   Sends notifications to a configurable Discord webhook.
*   Designed for lightweight, serverless deployment on Google Cloud Run (free tier).
*   Robust error handling and logging to Google Cloud Logging.

## Simplified Architecture

The bot operates with a simple, serverless architecture on Google Cloud:

*   **Cloud Scheduler:** Triggers the bot every minute (or as configured).
*   **Cloud Run:** Hosts the Go application. When triggered, it:
    1.  Fetches the latest deals from the RFD Hot Deals RSS feed.
    2.  Checks Firestore to determine which deals are new since the last run.
    3.  Processes new deals (extracts info, filters, formats).
    4.  Sends formatted deal notifications to the configured Discord webhook.
    5.  Updates Firestore with the timestamp of the last processed deal.
*   **Firestore:** Stores the timestamp of the last successfully processed deal, ensuring deals are not sent multiple times.

```mermaid
graph TD
    A[Cloud Scheduler (Every minute)] --> B(Cloud Run: Go Application)
    B --> C{Fetch RFD RSS Feed}
    C --> D{Check Firestore for last processed deal}
    D --> E{Identify new deals}
    E --> F{Process & Format Deals}
    F --> G[Send to Discord Webhook]
    G --> H{Update Firestore with new last processed deal timestamp}
    B <--> I[Firestore (Stores last_processed_deal timestamp)]
```

## Local Development

### Clone Repository

```bash
git clone https://your-repository-url/rfd-discord-bot.git
cd rfd-discord-bot
```
*(Replace `https://your-repository-url/rfd-discord-bot.git` with the actual URL of your repository)*

### Environment Setup (Go)

1.  **Install Go:** Download and install Go from the [official Go download page](https://golang.org/dl/).
2.  **Navigate to Directory:** Open your terminal and navigate to the cloned `rfd-discord-bot` directory.
    ```bash
    cd path/to/rfd-discord-bot
    ```
3.  **Install Dependencies:** Run the following command to download and install the necessary Go modules:
    ```bash
    go mod tidy
    ```

### Environment Variables for Local Testing

The application requires the following environment variables to run locally:

*   `GOOGLE_CLOUD_PROJECT`: Your Google Cloud Project ID.
*   `DISCORD_WEBHOOK_URL`: The Discord webhook URL where notifications will be sent.

It's recommended to create a `.env` file in the project root to store these variables. This file is included in `.gitignore` to prevent accidental commits of sensitive information.

**Create `.env` file:**
```bash
# .env
export GOOGLE_CLOUD_PROJECT="your-gcp-project-id"
export DISCORD_WEBHOOK_URL="your-discord-webhook-url"
```
Replace `"your-gcp-project-id"` and `"your-discord-webhook-url"` with your actual values.

**Load Environment Variables:**
Before running the application, source the `.env` file:
```bash
source .env
```
Alternatively, you can set these variables directly in your shell session:
```bash
export GOOGLE_CLOUD_PROJECT="your-gcp-project-id"
export DISCORD_WEBHOOK_URL="your-discord-webhook-url"
```

### Running Locally

Once the environment variables are set, you can run the application using:
```bash
go run main.go
```
Or, if all your Go files are in the `main` package and in the root directory:
```bash
go run .
```
This will start an HTTP server, typically on port `8080` (or the port specified by the `PORT` environment variable, which Cloud Run sets automatically).

To test the handler locally, you can send a GET request to `http://localhost:8080/` (or the specific path your handler listens on, if different) using a tool like `curl` or your web browser.
```bash
curl http://localhost:8080/
```
Note: While you can trigger it via HTTP locally, in the deployed Google Cloud environment, the primary trigger is Cloud Scheduler.

## Google Cloud Deployment (Detailed & Hand-holding)

These instructions will guide you through deploying the bot to Google Cloud Run.

### Prerequisites

1.  **Google Cloud Project:** You need an active Google Cloud Project. If you don't have one, create one at the [Google Cloud Console](https://console.cloud.google.com/).
2.  **`gcloud` CLI:** Install and initialize the Google Cloud CLI.
    *   Installation instructions: [Google Cloud SDK](https://cloud.google.com/sdk/docs/install)
    *   Authenticate and set your project:
        ```bash
        gcloud auth login
        gcloud config set project YOUR_PROJECT_ID
        ```
        Replace `YOUR_PROJECT_ID` with your actual project ID.

### Enable APIs

The bot requires several Google Cloud APIs to be enabled for your project:
*   Cloud Run API: For deploying and running the serverless application.
*   Cloud Firestore API: For database storage.
*   Cloud Scheduler API: For triggering the bot periodically.

Enable them using the following `gcloud` command:
```bash
gcloud services enable run.googleapis.com firestore.googleapis.com cloudscheduler.googleapis.com --project YOUR_PROJECT_ID
```
Replace `YOUR_PROJECT_ID` with your project ID.

### Create Firestore Instance

1.  Go to the [Firestore section](https://console.cloud.google.com/firestore) in the Google Cloud Console.
2.  If prompted, click **"Select Native Mode"** for your database.
3.  Choose a **location** for your Firestore database (e.g., `nam5 (United States)` or a region close to you/your users). This choice is permanent.
4.  Click **"Create Database"**.

The bot will automatically create the necessary collection (`bot_state`) and document (`last_processed_deal`) within Firestore when it runs for the first time.

### Configure Discord Webhook URL (as Environment Variable)

The `DISCORD_WEBHOOK_URL` is a sensitive piece of information and should be passed to the Cloud Run service as an environment variable during deployment.

**CRITICAL: DO NOT COMMIT THE ACTUAL WEBHOOK URL TO YOUR CODE REPOSITORY.**

You will provide this URL in the deployment command below.

### Deploy Cloud Run Service

Navigate to your project's root directory (`rfd-discord-bot`) in your terminal. Run the following command to deploy the service:

```bash
gcloud run deploy rfd-discord-bot \
    --source . \
    --platform managed \
    --region YOUR_CHOSEN_REGION \
    --allow-unauthenticated \
    --set-env-vars GOOGLE_CLOUD_PROJECT=YOUR_PROJECT_ID,DISCORD_WEBHOOK_URL=YOUR_DISCORD_WEBHOOK_URL \
    --project YOUR_PROJECT_ID
```

**Explanation of placeholders:**
*   `YOUR_CHOSEN_REGION`: The Google Cloud region where you want to deploy your service (e.g., `us-central1`, `europe-west1`). Choose a region that supports Cloud Run and Firestore, and ideally is close to your users or the RFD servers.
*   `YOUR_PROJECT_ID`: Your Google Cloud Project ID.
*   `YOUR_DISCORD_WEBHOOK_URL`: Your actual Discord webhook URL. **Remember to replace this placeholder with your real URL when running the command.**

**Notes on the command:**
*   `--source .`: Tells Cloud Run to build and deploy the code from the current directory.
*   `--platform managed`: Specifies using the fully managed Cloud Run environment.
*   `--allow-unauthenticated`: This makes your Cloud Run service publicly accessible. This is used here to allow Cloud Scheduler to invoke it easily without complex authentication setup for this simple use case. For sensitive services, you should use IAM-based invocation (i.e., remove this flag and configure IAM permissions for Cloud Scheduler to invoke the service).
*   `--set-env-vars`: Sets environment variables for the Cloud Run service.

After the deployment is successful, the command will output the **Service URL**. Copy this URL, as you'll need it for setting up Cloud Scheduler.

### Set up Cloud Scheduler

Cloud Scheduler will periodically trigger your Cloud Run service to check for new deals.

You can create a Cloud Scheduler job via the GCP Console or using `gcloud`.

**Using `gcloud` (Recommended):**
```bash
gcloud scheduler jobs create http rfd-bot-trigger \
    --schedule="*/5 * * * *" \
    --uri=YOUR_CLOUD_RUN_SERVICE_URL \
    --http-method=GET \
    --time-zone="America/Toronto" \
    --description="Triggers the RFD Discord Bot every 5 minutes" \
    --project YOUR_PROJECT_ID
```

**Explanation:**
*   `rfd-bot-trigger`: A name for your scheduler job.
*   `--schedule="*/5 * * * *"`: Sets the job to run every 5 minutes using cron syntax. You can adjust this (e.g., `* * * * *` for every minute, but be mindful of RSS feed politeness and potential rate limits).
*   `--uri=YOUR_CLOUD_RUN_SERVICE_URL`: **Replace this with the Service URL** you obtained after deploying your Cloud Run service.
*   `--http-method=GET`: The HTTP method to use. `GET` is fine for this bot's main handler.
*   `--time-zone="America/Toronto"`: Set this to your preferred timezone. This affects how the schedule is interpreted.
*   `--project YOUR_PROJECT_ID`: Your Google Cloud Project ID.

**Using GCP Console:**
1.  Go to [Cloud Scheduler](https://console.cloud.google.com/cloudscheduler) in the GCP Console.
2.  Click **"Create Job"**.
3.  Configure the job:
    *   **Name:** e.g., `rfd-bot-trigger`
    *   **Region:** Choose a region for the scheduler job.
    *   **Description:** (Optional) e.g., "Triggers RFD Discord Bot"
    *   **Frequency:** Enter the cron schedule (e.g., `*/5 * * * *` for every 5 minutes).
    *   **Timezone:** Select your timezone (e.g., `America/Toronto`).
    *   **Target type:** HTTP
    *   **URL:** Paste the Cloud Run **Service URL**.
    *   **HTTP Method:** GET (or POST, depending on your handler; GET is fine here).
    *   **Auth header:** Select "None" (because we used `--allow-unauthenticated` for Cloud Run). If you deployed a private service, you'd configure OIDC or OAuth here.
4.  Click **"Create"**.

### Configure IAM Permissions

Proper IAM permissions ensure your Cloud services can interact securely.

**1. Cloud Run Service Account to Firestore:**
The Cloud Run service needs permission to read from and write to Firestore.
*   **Find Service Account:** When you deploy a Cloud Run service, it uses a service account. By default, this is the Compute Engine default service account (`PROJECT_NUMBER-compute@developer.gserviceaccount.com`). You can see the exact service account used in the Cloud Run service details page in the GCP Console, under the "Security" or "Details" tab.
*   **Grant Role:** Grant this service account the "Cloud Datastore User" role (which includes Firestore permissions) or the more specific "Firestore User" (`roles/firestore.user`) role.

```bash
gcloud projects add-iam-policy-binding YOUR_PROJECT_ID \
    --member="serviceAccount:YOUR_CLOUD_RUN_SERVICE_ACCOUNT_EMAIL" \
    --role="roles/datastore.user"
```
Replace `YOUR_PROJECT_ID` with your project ID and `YOUR_CLOUD_RUN_SERVICE_ACCOUNT_EMAIL` with the email of the service account your Cloud Run service uses.

**2. Cloud Scheduler Service Account to Invoke Cloud Run:**
*   If you used `--allow-unauthenticated` when deploying your Cloud Run service, this step is **generally not needed** for the invocation itself, as the service is public.
*   However, if you deploy your Cloud Run service as private (by omitting `--allow-unauthenticated`), the Cloud Scheduler service account needs permission to invoke it.
*   Cloud Scheduler jobs run under a service account. By default, this is the App Engine default service account (`YOUR_PROJECT_ID@appspot.gserviceaccount.com`). If you use a custom service account for Scheduler, ensure it has the "Cloud Run Invoker" (`roles/run.invoker`) role for your Cloud Run service.

For a private service, the command would look like:
```bash
# Example for a private service - not strictly needed if --allow-unauthenticated was used
gcloud run services add-iam-policy-binding rfd-discord-bot \
    --member="serviceAccount:YOUR_SCHEDULER_SERVICE_ACCOUNT_EMAIL" \
    --role="roles/run.invoker" \
    --region=YOUR_CHOSEN_REGION \
    --platform=managed \
    --project=YOUR_PROJECT_ID
```
Replace placeholders accordingly. For simplicity with `--allow-unauthenticated`, this is more of an FYI.

## Usage

Once deployed and Cloud Scheduler is active (it might take a minute for the first scheduled run after creation), the bot runs automatically. New deals from the RFD Hot Deals RSS feed will be processed and posted to your configured Discord channel.

## Error Logging & Alerting

### Cloud Logging

All standard output from your Go application (e.g., using `log.Printf`, `fmt.Println`) when running on Cloud Run is automatically captured by [Google Cloud Logging](https://console.cloud.google.com/logs/viewer).

To view logs:
1.  Go to the GCP Console.
2.  Navigate to "Logging" > "Logs Explorer".
3.  You can filter logs by your Cloud Run service name:
    *   In the query builder, select "Cloud Run Revision" as the resource type.
    *   Then select your service name (`rfd-discord-bot`) and revision.

This is the primary place to check for errors or operational messages from your bot.

### Critical Alerts (How to Set Up)

For proactive monitoring, you can set up alerts in [Google Cloud Monitoring](https://console.cloud.google.com/monitoring) for critical errors.

1.  **Navigate to Alerting:** In the GCP Console, go to "Monitoring" > "Alerting".
2.  **Create Log-Based Metrics (Optional but Recommended for Specific Errors):**
    *   Go to "Logging" > "Log-based Metrics".
    *   Click "Create Metric".
    *   Define a filter for specific error messages you want to track (e.g., `resource.type="cloud_run_revision" AND resource.labels.service_name="rfd-discord-bot" AND severity=ERROR AND textPayload:"Failed to fetch RSS feed"`).
    *   Give the metric a name (e.g., `rfd-bot-rss-fetch-errors`).
3.  **Create an Alert Policy:**
    *   In "Monitoring" > "Alerting", click "+ Create Policy".
    *   **Add Condition:**
        *   Select the metric you want to alert on. This could be a general error count for your Cloud Run service or a specific log-based metric you created.
        *   For example, to alert on any error logs from your service:
            *   Target: `Cloud Run Revision` > `Log Entries`
            *   Filter for your service and `severity=ERROR`.
            *   Configure the condition (e.g., "Alert if count of error logs > 0 in 5 minutes").
        *   If using a log-based metric:
            *   Target: Search for your custom log-based metric name.
            *   Configure the condition (e.g., "Alert if value of metric > 0 for 5 minutes").
    *   **Notifications:**
        *   Choose or create notification channels (e.g., Email, SMS, PagerDuty, Slack via Pub/Sub).
    *   **Name and Save:** Give your alert policy a descriptive name.

**Specific Error Types to Consider Alerting On:**
*   Persistent "Failed to fetch RSS feed" errors.
*   "Discord webhook failed" errors (could indicate an invalid URL or Discord API issues).
*   "Firestore operation error" (read/write failures).
*   Any unhandled panics or critical errors logged by the application.

Cloud Logging automatically manages log retention according to configured policies.

## Troubleshooting

### Common Issues

*   **Deals Not Appearing in Discord:**
    1.  **Check Cloud Scheduler:** Go to Cloud Scheduler in the GCP Console. Verify the job (`rfd-bot-trigger`) status. Look at "Last run" and "Result". If it's failing, check its logs.
    2.  **Check Cloud Run Logs:** Go to Cloud Logging and filter for your `rfd-discord-bot` service. Look for any errors related to RSS fetching, Firestore operations, or sending messages to Discord.
    3.  **Discord Webhook URL:** Ensure the `DISCORD_WEBHOOK_URL` environment variable in your Cloud Run service configuration is correct and the webhook is still valid in Discord.
    4.  **Firestore Data:** Check Firestore to see if the `last_processed_deal` document in the `bot_state` collection is being updated. If it's stuck, it might indicate an issue processing a specific deal.

*   **Authentication/Permission Errors in Logs:**
    1.  **Cloud Run to Firestore:** Double-check that the Cloud Run service's service account has the "Cloud Datastore User" (or "Firestore User") role.
    2.  **Cloud Scheduler to Cloud Run (for private services):** If your service is private, ensure the Scheduler's service account has "Cloud Run Invoker" permission. (Not applicable if `--allow-unauthenticated` was used).

*   **Incorrect Environment Variables:**
    *   Verify `GOOGLE_CLOUD_PROJECT` and `DISCORD_WEBHOOK_URL` are correctly set in the Cloud Run service's environment variables (check the "Revisions" tab of your service in Cloud Run, select the active revision, and look at "Variables").

### How to Diagnose

1.  **Start with Cloud Logging:** This is your most important tool. Filter by your Cloud Run service (`rfd-discord-bot`). Look for `ERROR` or `CRITICAL` severity logs.
2.  **Check Cloud Scheduler Job Status:** Ensure the job is running successfully and on schedule.
3.  **Test Cloud Run Endpoint Manually:** You can try to invoke your Cloud Run service URL directly from your browser or `curl` to see if it responds or logs any immediate errors.
4.  **Examine Firestore Data:** Look at the `bot_state/last_processed_deal` document in Firestore to understand what the bot last processed.

---

This README provides a comprehensive guide for developers to understand, set up, deploy, and manage the RFD Hot Deals Discord Bot on Google Cloud.