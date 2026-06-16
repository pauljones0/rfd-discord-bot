package dev.swordswallower;

import android.app.Notification;
import android.app.PendingIntent;
import android.os.Bundle;
import android.os.Parcelable;
import android.service.notification.NotificationListenerService;
import android.service.notification.StatusBarNotification;
import android.util.Log;

import org.json.JSONArray;
import org.json.JSONException;
import org.json.JSONObject;

import java.util.LinkedHashMap;
import java.util.Locale;
import java.util.Map;
import java.util.regex.Pattern;
import java.util.regex.PatternSyntaxException;

public class DiscordNotificationListenerService extends NotificationListenerService {
    private static final String TAG = "SwordsListener";
    private static final int MAX_SEEN = 256;

    private final Map<String, Boolean> seen = new LinkedHashMap<String, Boolean>(MAX_SEEN + 1, 0.75f, true) {
        @Override
        protected boolean removeEldestEntry(Map.Entry<String, Boolean> eldest) {
            return size() > MAX_SEEN;
        }
    };

    @Override
    public void onListenerConnected() {
        try {
            StatusBarNotification[] activeNotifications = getActiveNotifications();
            if (activeNotifications == null) {
                Log.i(TAG, "Listener connected with no active notifications");
                return;
            }
            Log.i(TAG, "Listener connected; active notifications=" + activeNotifications.length);
            for (StatusBarNotification sbn : activeNotifications) {
                onNotificationPosted(sbn);
            }
        } catch (Exception e) {
            Log.e(TAG, "Error in onListenerConnected", e);
            forwardListenerError("onListenerConnected", e);
        }
    }

    @Override
    public void onNotificationPosted(StatusBarNotification sbn) {
        try {
            Prefs prefs = new Prefs(this);
            if (!prefs.isTargetPackage(sbn.getPackageName())) {
                return;
            }
            Log.i(TAG, "Processing notification key=" + sbn.getKey());

            String seenKey = sbn.getKey() + ":" + sbn.getPostTime();
            synchronized (seen) {
                if (seen.containsKey(seenKey)) {
                    return;
                }
                seen.put(seenKey, Boolean.TRUE);
            }

            Notification notification = sbn.getNotification();
            if (notification == null) {
                return;
            }

            JSONObject event = buildEvent(sbn, notification);
            ActionResult actionResult = new ActionResult();
            if (prefs.shouldAutoAction()) {
                actionResult = sendMatchingAction(notification, prefs.getActionRegex());
            }
            if (!actionResult.sent && prefs.shouldCancelFallback()) {
                actionResult.cancelFallbackUsed = cancelPostedNotification(sbn);
            }

            try {
                event.put("markRead", actionResult.toJson());
            } catch (JSONException e) {
                Log.w(TAG, "Failed to add action result", e);
            }

            EventForwarder.forward(this, event, result -> {
                if (shouldClearNotification(result)) {
                    boolean cleared = cancelPostedNotification(sbn);
                    Log.i(TAG, "Server requested notification clear key=" + sbn.getKey()
                            + " cleared=" + cleared);
                }
            });
            Log.i(TAG, "Forwarded notification key=" + sbn.getKey()
                    + " markReadSent=" + actionResult.sent
                    + " markReadReason=" + actionResult.reason);
        } catch (Exception e) {
            Log.e(TAG, "Error in onNotificationPosted", e);
            forwardListenerError("onNotificationPosted", e);
        }
    }

    private JSONObject buildEvent(StatusBarNotification sbn, Notification notification) {
        JSONObject event = new JSONObject();
        try {
            event.put("type", "notification");
            event.put("receivedAt", System.currentTimeMillis());
            event.put("packageName", sbn.getPackageName());
            event.put("notificationKey", sbn.getKey());
            event.put("tag", sbn.getTag());
            event.put("postTime", sbn.getPostTime());

            if (notification.tickerText != null) {
                event.put("tickerText", stringValue(notification.tickerText));
            }

            JSONObject extras = extractExtras(notification.extras);
            if (notification.contentIntent != null) {
                extras.put("contentIntent", notification.contentIntent.toString());
            }
            event.put("extras", extras);
            event.put("actions", extractActions(notification.actions));
        } catch (JSONException e) {
            Log.w(TAG, "Failed to build event", e);
        }
        return event;
    }

    private JSONObject extractExtras(Bundle extras) throws JSONException {
        JSONObject json = new JSONObject();
        if (extras == null) {
            return json;
        }

        putCharSequence(json, "title", extras.getCharSequence(Notification.EXTRA_TITLE));
        putCharSequence(json, "titleBig", extras.getCharSequence(Notification.EXTRA_TITLE_BIG));
        putCharSequence(json, "text", extras.getCharSequence(Notification.EXTRA_TEXT));
        putCharSequence(json, "bigText", extras.getCharSequence(Notification.EXTRA_BIG_TEXT));
        putCharSequence(json, "subText", extras.getCharSequence(Notification.EXTRA_SUB_TEXT));
        putCharSequence(json, "summaryText", extras.getCharSequence(Notification.EXTRA_SUMMARY_TEXT));
        putCharSequence(json, "infoText", extras.getCharSequence(Notification.EXTRA_INFO_TEXT));
        putCharSequence(json, "conversationTitle", extras.getCharSequence(Notification.EXTRA_CONVERSATION_TITLE));
        putTextLines(json, "textLines", extras.getCharSequenceArray(Notification.EXTRA_TEXT_LINES));
        putMessages(json, "messages", extras.getParcelableArray(Notification.EXTRA_MESSAGES));
        if (extras.containsKey(Notification.EXTRA_IS_GROUP_CONVERSATION)) {
            json.put("isGroupConversation", extras.getBoolean(Notification.EXTRA_IS_GROUP_CONVERSATION));
        }
        putDeepLink(json, "link", firstExtraValue(extras, "link", "url", "android.link", "android.url"));
        putDeepLink(json, "uri", firstExtraValue(extras, "uri", "android.intent.extra.STREAM"));
        putDeepLink(json, "dataLink", firstExtraValue(extras, "dataLink", "dataUri", "android.intent.extra.TEXT"));

        // Extract embed image if present
        if (extras.containsKey(Notification.EXTRA_PICTURE)) {
            try {
                android.graphics.Bitmap bmp = (android.graphics.Bitmap) extras.get(Notification.EXTRA_PICTURE);
                if (bmp != null) {
                    java.io.ByteArrayOutputStream baos = new java.io.ByteArrayOutputStream();
                    bmp.compress(android.graphics.Bitmap.CompressFormat.JPEG, 70, baos);
                    byte[] imageBytes = baos.toByteArray();
                    json.put("pictureBase64", android.util.Base64.encodeToString(imageBytes, android.util.Base64.NO_WRAP));
                }
            } catch (Exception e) {
                Log.w(TAG, "Failed to encode picture", e);
            }
        }

        return json;
    }

    private void putDeepLink(JSONObject json, String key, Object value) throws JSONException {
        if (value != null) {
            json.put(key, value.toString());
        }
    }

    private Object firstExtraValue(Bundle extras, String... keys) {
        for (String key : keys) {
            if (extras.containsKey(key)) {
                Object value = extras.get(key);
                if (value != null) {
                    return value;
                }
            }
        }
        return null;
    }

    private void putMessages(JSONObject json, String key, Parcelable[] messages) throws JSONException {
        if (messages == null) {
            return;
        }
        JSONArray array = new JSONArray();
        for (Parcelable p : messages) {
            if (p instanceof Bundle) {
                Bundle m = (Bundle) p;
                JSONObject msg = new JSONObject();
                putCharSequence(msg, "text", m.getCharSequence("text"));
                msg.put("time", m.getLong("time"));
                putCharSequence(msg, "sender", m.getCharSequence("sender"));
                putCharSequence(msg, "type", m.getString("type"));
                putCharSequence(msg, "uri", m.getString("uri"));
                array.put(msg);
            }
        }
        json.put(key, array);
    }

    private void putTextLines(JSONObject json, String key, CharSequence[] lines) throws JSONException {
        if (lines == null) {
            return;
        }
        JSONArray array = new JSONArray();
        for (CharSequence line : lines) {
            if (line != null) {
                array.put(stringValue(line));
            }
        }
        json.put(key, array);
    }

    private void putCharSequence(JSONObject json, String key, CharSequence value) throws JSONException {
        if (value != null) {
            json.put(key, stringValue(value));
        }
    }

    private String stringValue(CharSequence value) {
        if (value == null) return "";
        return value.toString();
    }

    private ActionResult sendMatchingAction(Notification notification, String regex) {
        ActionResult result = new ActionResult();
        if (notification.actions == null || regex == null || regex.isEmpty()) {
            result.reason = "no_actions_or_regex";
            return result;
        }

        Pattern pattern;
        try {
            pattern = Pattern.compile(regex);
        } catch (PatternSyntaxException e) {
            result.error = "invalid_regex: " + e.getMessage();
            result.reason = "regex_error";
            return result;
        }

        for (Notification.Action action : notification.actions) {
            if (action.title != null && pattern.matcher(action.title).matches()) {
                if (action.actionIntent != null) {
                    try {
                        action.actionIntent.send();
                        result.sent = true;
                        result.matchedTitle = action.title.toString();
                        result.reason = "sent";
                        return result;
                    } catch (PendingIntent.CanceledException e) {
                        result.error = "intent_canceled: " + e.getMessage();
                    }
                } else {
                    result.reason = "no_intent";
                }
            }
        }

        result.reason = "no_match";
        return result;
    }

    private boolean cancelPostedNotification(StatusBarNotification sbn) {
        try {
            cancelNotification(sbn.getKey());
            return true;
        } catch (Exception e) {
            Log.w(TAG, "Failed to cancel notification", e);
            return false;
        }
    }

    private boolean shouldClearNotification(EventForwarder.Result result) {
        if (result == null || !result.accepted() || result.response == null) {
            return false;
        }
        return result.response.optBoolean("clearNotification", false);
    }

    private void forwardListenerError(String stage, Exception error) {
        JSONObject event = new JSONObject();
        try {
            event.put("type", "listener_error");
            event.put("receivedAt", System.currentTimeMillis());
            event.put("source", "swordswallower");
            event.put("stage", stage);
            event.put("message", error.getMessage());
            event.put("error", error.toString());
        } catch (JSONException e) {
            Log.w(TAG, "Failed to build listener error event", e);
        }
        EventForwarder.forward(this, event);
    }

    private JSONArray extractActions(Notification.Action[] actions) throws JSONException {
        JSONArray json = new JSONArray();
        if (actions == null) {
            return json;
        }
        for (Notification.Action action : actions) {
            JSONObject obj = new JSONObject();
            obj.put("title", action.title);
            obj.put("hasIntent", action.actionIntent != null);
            json.put(obj);
        }
        return json;
    }

    private static class ActionResult {
        boolean sent = false;
        boolean cancelFallbackUsed = false;
        String matchedTitle = "";
        String reason = "";
        String error = "";

        JSONObject toJson() throws JSONException {
            JSONObject json = new JSONObject();
            json.put("sent", sent);
            json.put("cancelFallbackUsed", cancelFallbackUsed);
            json.put("matchedTitle", matchedTitle);
            json.put("reason", reason);
            if (error.length() > 0) {
                json.put("error", error);
            }
            return json;
        }
    }
}
