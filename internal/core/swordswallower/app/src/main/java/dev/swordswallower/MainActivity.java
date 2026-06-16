package dev.swordswallower;

import android.app.Activity;
import android.content.ComponentName;
import android.content.Intent;
import android.os.Bundle;
import android.provider.Settings;
import android.text.InputType;
import android.text.TextUtils;
import android.widget.Button;
import android.widget.CheckBox;
import android.widget.EditText;
import android.widget.LinearLayout;
import android.widget.ScrollView;
import android.widget.TextView;
import android.widget.Toast;

public class MainActivity extends Activity {
    private static final String EXTRA_WEBHOOK_URL = "webhookUrl";
    private static final String EXTRA_WEBHOOK_SECRET = "webhookSecret";
    private static final String EXTRA_TARGET_PACKAGE = "targetPackage";
    private static final String EXTRA_ACTION_REGEX = "actionRegex";
    private static final String EXTRA_AUTO_ACTION = "autoAction";
    private static final String EXTRA_CANCEL_FALLBACK = "cancelFallback";

    private TextView listenerStatus;
    private EditText webhookUrl;
    private EditText webhookSecret;
    private EditText targetPackage;
    private EditText actionRegex;
    private CheckBox autoAction;
    private CheckBox cancelFallback;

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        Prefs prefs = new Prefs(this);
        applyIntentConfig(getIntent(), prefs);

        ScrollView scroll = new ScrollView(this);
        LinearLayout root = new LinearLayout(this);
        root.setOrientation(LinearLayout.VERTICAL);
        root.setPadding(dp(16), dp(16), dp(16), dp(16));
        scroll.addView(root);

        TextView title = new TextView(this);
        title.setText("Swordswallower");
        title.setTextSize(24);
        root.addView(title, fillWrap());

        listenerStatus = new TextView(this);
        root.addView(listenerStatus, fillWrap());

        webhookUrl = input("Webhook URL", prefs.getWebhookUrl(),
                InputType.TYPE_CLASS_TEXT | InputType.TYPE_TEXT_VARIATION_URI);
        root.addView(label("Webhook URL"));
        root.addView(webhookUrl, fillWrap());

        webhookSecret = input("Optional shared secret", prefs.getWebhookSecret(),
                InputType.TYPE_CLASS_TEXT | InputType.TYPE_TEXT_VARIATION_PASSWORD);
        root.addView(label("Optional shared secret"));
        root.addView(webhookSecret, fillWrap());

        targetPackage = input("Target package(s), comma-separated", prefs.getTargetPackage(), InputType.TYPE_CLASS_TEXT);
        root.addView(label("Target package(s), comma-separated"));
        root.addView(targetPackage, fillWrap());

        actionRegex = input("Action match regex", prefs.getActionRegex(), InputType.TYPE_CLASS_TEXT);
        root.addView(label("Action match regex"));
        root.addView(actionRegex, fillWrap());

        autoAction = new CheckBox(this);
        autoAction.setText("Send matching notification action");
        autoAction.setChecked(prefs.shouldAutoAction());
        root.addView(autoAction, fillWrap());

        cancelFallback = new CheckBox(this);
        cancelFallback.setText("Cancel notification if no action matches");
        cancelFallback.setChecked(prefs.shouldCancelFallback());
        root.addView(cancelFallback, fillWrap());

        Button save = button("Save");
        save.setOnClickListener(v -> save());
        root.addView(save, fillWrap());

        Button settings = button("Open notification access");
        settings.setOnClickListener(v -> startActivity(new Intent(Settings.ACTION_NOTIFICATION_LISTENER_SETTINGS)));
        root.addView(settings, fillWrap());

        Button test = button("Send test event");
        test.setOnClickListener(v -> EventForwarder.forwardTest(this));
        root.addView(test, fillWrap());

        setContentView(scroll);
        refreshListenerStatus();
    }

    @Override
    protected void onNewIntent(Intent intent) {
        super.onNewIntent(intent);
        setIntent(intent);
        Prefs prefs = new Prefs(this);
        if (applyIntentConfig(intent, prefs)) {
            updateFields(prefs);
            Toast.makeText(this, "Configured", Toast.LENGTH_SHORT).show();
        }
    }

    @Override
    protected void onResume() {
        super.onResume();
        refreshListenerStatus();
    }

    private void save() {
        new Prefs(this).save(
                webhookUrl.getText().toString(),
                webhookSecret.getText().toString(),
                targetPackage.getText().toString(),
                actionRegex.getText().toString(),
                autoAction.isChecked(),
                cancelFallback.isChecked());
        Toast.makeText(this, "Saved", Toast.LENGTH_SHORT).show();
    }

    private boolean applyIntentConfig(Intent intent, Prefs prefs) {
        if (intent == null || intent.getExtras() == null) {
            return false;
        }

        Bundle extras = intent.getExtras();
        boolean hasConfig = extras.containsKey(EXTRA_WEBHOOK_URL)
                || extras.containsKey(EXTRA_WEBHOOK_SECRET)
                || extras.containsKey(EXTRA_TARGET_PACKAGE)
                || extras.containsKey(EXTRA_ACTION_REGEX)
                || extras.containsKey(EXTRA_AUTO_ACTION)
                || extras.containsKey(EXTRA_CANCEL_FALLBACK);
        if (!hasConfig) {
            return false;
        }

        prefs.save(
                extras.getString(EXTRA_WEBHOOK_URL, prefs.getWebhookUrl()),
                extras.getString(EXTRA_WEBHOOK_SECRET, prefs.getWebhookSecret()),
                extras.getString(EXTRA_TARGET_PACKAGE, prefs.getTargetPackage()),
                extras.getString(EXTRA_ACTION_REGEX, prefs.getActionRegex()),
                extras.getBoolean(EXTRA_AUTO_ACTION, prefs.shouldAutoAction()),
                extras.getBoolean(EXTRA_CANCEL_FALLBACK, prefs.shouldCancelFallback()));
        return true;
    }

    private void updateFields(Prefs prefs) {
        if (webhookUrl == null) {
            return;
        }
        webhookUrl.setText(prefs.getWebhookUrl());
        webhookSecret.setText(prefs.getWebhookSecret());
        targetPackage.setText(prefs.getTargetPackage());
        actionRegex.setText(prefs.getActionRegex());
        autoAction.setChecked(prefs.shouldAutoAction());
        cancelFallback.setChecked(prefs.shouldCancelFallback());
    }

    private void refreshListenerStatus() {
        String status = isNotificationListenerEnabled()
                ? "Notification access: enabled"
                : "Notification access: disabled";
        listenerStatus.setText(status);
    }

    private boolean isNotificationListenerEnabled() {
        ComponentName expected = new ComponentName(this, DiscordNotificationListenerService.class);
        String enabled = Settings.Secure.getString(
                getContentResolver(),
                "enabled_notification_listeners");
        if (enabled == null || enabled.length() == 0) {
            return false;
        }

        TextUtils.SimpleStringSplitter splitter = new TextUtils.SimpleStringSplitter(':');
        splitter.setString(enabled);
        while (splitter.hasNext()) {
            ComponentName actual = ComponentName.unflattenFromString(splitter.next());
            if (expected.equals(actual)) {
                return true;
            }
        }
        return false;
    }

    private EditText input(String hint, String value, int inputType) {
        EditText input = new EditText(this);
        input.setHint(hint);
        input.setText(value);
        input.setSingleLine(true);
        input.setInputType(inputType);
        return input;
    }

    private TextView label(String text) {
        TextView label = new TextView(this);
        label.setText(text);
        label.setPadding(0, dp(12), 0, 0);
        return label;
    }

    private Button button(String text) {
        Button button = new Button(this);
        button.setText(text);
        return button;
    }

    private LinearLayout.LayoutParams fillWrap() {
        LinearLayout.LayoutParams params = new LinearLayout.LayoutParams(
                LinearLayout.LayoutParams.MATCH_PARENT,
                LinearLayout.LayoutParams.WRAP_CONTENT);
        params.setMargins(0, dp(4), 0, dp(4));
        return params;
    }

    private int dp(int value) {
        return (int) (value * getResources().getDisplayMetrics().density + 0.5f);
    }
}
