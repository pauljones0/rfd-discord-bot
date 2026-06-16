package dev.swordswallower;

import android.content.Context;
import android.util.Log;

import org.json.JSONException;
import org.json.JSONObject;

import java.io.ByteArrayOutputStream;
import java.io.InputStream;
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

    interface Callback {
        void onComplete(Result result);
    }

    static final class Result {
        final int code;
        final JSONObject response;
        final String error;

        Result(int code, JSONObject response, String error) {
            this.code = code;
            this.response = response;
            this.error = error;
        }

        boolean accepted() {
            return code >= 200 && code < 300;
        }
    }

    static void forward(Context context, JSONObject event) {
        forward(context, event, null);
    }

    static void forward(Context context, JSONObject event, Callback callback) {
        Prefs prefs = new Prefs(context);
        String webhookUrl = prefs.getWebhookUrl();
        if (webhookUrl.length() == 0) {
            Log.d(TAG, "Webhook URL is empty; event not forwarded");
            if (callback != null) {
                callback.onComplete(new Result(0, null, "webhook_url_empty"));
            }
            return;
        }

        String secret = prefs.getWebhookSecret();
        EXECUTOR.execute(() -> {
            Result result = post(webhookUrl, secret, event);
            if (callback != null) {
                callback.onComplete(result);
            }
        });
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

    private static Result post(String webhookUrl, String secret, JSONObject event) {
        HttpURLConnection connection = null;
        try {
            byte[] body = event.toString().getBytes(StandardCharsets.UTF_8);
            connection = (HttpURLConnection) new URL(webhookUrl).openConnection();
            connection.setConnectTimeout(5000);
            connection.setReadTimeout(15000);
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
            JSONObject response = parseResponse(connection, code);
            if (code < 200 || code >= 300) {
                Log.w(TAG, "Webhook returned HTTP " + code);
            } else {
                Log.i(TAG, "Webhook accepted HTTP " + code);
            }
            return new Result(code, response, "");
        } catch (Exception e) {
            Log.w(TAG, "Failed to forward event", e);
            return new Result(0, null, e.toString());
        } finally {
            if (connection != null) {
                connection.disconnect();
            }
        }
    }

    private static JSONObject parseResponse(HttpURLConnection connection, int code) {
        InputStream in = null;
        try {
            in = code >= 200 && code < 400 ? connection.getInputStream() : connection.getErrorStream();
            if (in == null) {
                return null;
            }
            ByteArrayOutputStream out = new ByteArrayOutputStream();
            byte[] buffer = new byte[4096];
            int read;
            while ((read = in.read(buffer)) != -1) {
                out.write(buffer, 0, read);
            }
            String body = new String(out.toByteArray(), StandardCharsets.UTF_8).trim();
            if (body.length() == 0 || !body.startsWith("{")) {
                return null;
            }
            return new JSONObject(body);
        } catch (Exception e) {
            Log.w(TAG, "Failed to parse webhook response", e);
            return null;
        } finally {
            if (in != null) {
                try {
                    in.close();
                } catch (Exception ignored) {
                }
            }
        }
    }
}
