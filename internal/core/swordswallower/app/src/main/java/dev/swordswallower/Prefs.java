package dev.swordswallower;

import android.content.Context;
import android.content.SharedPreferences;

final class Prefs {
    static final String DEFAULT_TARGET_PACKAGE = "com.discord";
    private static final String DEFAULT_ACTION_REGEX =
            "(?i).*(mark\\s*(as\\s*)?read|read\\s*already|already\\s*read).*";

    private static final String PREFS = "swordswallower";
    private static final String KEY_WEBHOOK_URL = "webhook_url";
    private static final String KEY_WEBHOOK_SECRET = "webhook_secret";
    private static final String KEY_TARGET_PACKAGE = "target_package";
    private static final String KEY_ACTION_REGEX = "action_regex";
    private static final String KEY_AUTO_ACTION = "auto_action";
    private static final String KEY_CANCEL_FALLBACK = "cancel_fallback";

    private final SharedPreferences prefs;

    Prefs(Context context) {
        prefs = context.getApplicationContext().getSharedPreferences(PREFS, Context.MODE_PRIVATE);
    }

    String getWebhookUrl() {
        return prefs.getString(KEY_WEBHOOK_URL, "");
    }

    String getWebhookSecret() {
        return prefs.getString(KEY_WEBHOOK_SECRET, "");
    }

    String getTargetPackage() {
        return prefs.getString(KEY_TARGET_PACKAGE, DEFAULT_TARGET_PACKAGE);
    }

    String getActionRegex() {
        return prefs.getString(KEY_ACTION_REGEX, DEFAULT_ACTION_REGEX);
    }

    boolean shouldAutoAction() {
        return prefs.getBoolean(KEY_AUTO_ACTION, true);
    }

    boolean shouldCancelFallback() {
        return prefs.getBoolean(KEY_CANCEL_FALLBACK, false);
    }

    void save(
            String webhookUrl,
            String webhookSecret,
            String targetPackage,
            String actionRegex,
            boolean autoAction,
            boolean cancelFallback) {
        prefs.edit()
                .putString(KEY_WEBHOOK_URL, clean(webhookUrl))
                .putString(KEY_WEBHOOK_SECRET, clean(webhookSecret))
                .putString(KEY_TARGET_PACKAGE, fallback(clean(targetPackage), DEFAULT_TARGET_PACKAGE))
                .putString(KEY_ACTION_REGEX, fallback(clean(actionRegex), DEFAULT_ACTION_REGEX))
                .putBoolean(KEY_AUTO_ACTION, autoAction)
                .putBoolean(KEY_CANCEL_FALLBACK, cancelFallback)
                .commit();
    }

    private static String clean(String value) {
        return value == null ? "" : value.trim();
    }

    private static String fallback(String value, String fallback) {
        return value.length() == 0 ? fallback : value;
    }
}
