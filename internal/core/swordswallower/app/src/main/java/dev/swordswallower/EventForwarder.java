package dev.swordswallower;

import android.content.Context;
import android.util.Log;

import org.json.JSONException;
import org.json.JSONObject;

import java.io.OutputStream;
import java.net.HttpURLConnection;
import java.net.URL;
import java.nio.charset.StandardCharsets;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;

final class EventForwarder {
    private static final String TAG = "SwordsForwarder";
    private static final ExecutorService EXECUTOR = Executors.newSingleThreadExecutor();

    private EventForwarder() {
    }

    static void forward(Context context, JSONObject event) {
        Prefs prefs = new Prefs(context);
        String webhookUrl = prefs.getWebhookUrl();
        if (webhookUrl.length() == 0) {
            Log.d(TAG, "Webhook URL is empty; event not forwarded");
            return;
        }

        String secret = prefs.getWebhookSecret();
        EXECUTOR.execute(() -> post(webhookUrl, secret, event));
    }

    static void forwardTest(Context context) {
        JSONObject event = new JSONObject();
        try {
            event.put("type", "test");
            event.put("receivedAt", System.currentTimeMillis());
            event.put("source", "swordswallower");
            event.put("message", "Swordswallower test event");
        } catch (JSONException e) {
            Log.w(TAG, "Failed to build test event", e);
        }
        forward(context, event);
    }

    private static void post(String webhookUrl, String secret, JSONObject event) {
        HttpURLConnection connection = null;
        try {
            byte[] body = event.toString().getBytes(StandardCharsets.UTF_8);
            connection = (HttpURLConnection) new URL(webhookUrl).openConnection();
            connection.setConnectTimeout(5000);
            connection.setReadTimeout(5000);
            connection.setDoOutput(true);
            connection.setRequestMethod("POST");
            connection.setRequestProperty("Content-Type", "application/json; charset=utf-8");
            connection.setRequestProperty("Accept", "application/json");
            if (secret.length() > 0) {
                connection.setRequestProperty("Authorization", "Bearer " + secret);
                connection.setRequestProperty("X-Swordswallower-Secret", secret);
            }

            try (OutputStream out = connection.getOutputStream()) {
                out.write(body);
            }

            int code = connection.getResponseCode();
            if (code < 200 || code >= 300) {
                Log.w(TAG, "Webhook returned HTTP " + code);
            } else {
                Log.i(TAG, "Webhook accepted HTTP " + code);
            }
        } catch (Exception e) {
            Log.w(TAG, "Failed to forward event", e);
        } finally {
            if (connection != null) {
                connection.disconnect();
            }
        }
    }
}
