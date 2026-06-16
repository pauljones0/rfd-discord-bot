# Swordswallower Notification Enhancements: WHAT TO EXPECT

The `swordswallower` Android notification listener has been updated to provide significantly more detail, specifically to solve issues with truncated text and missing deep links.

## New JSON Fields

When the bot receives a notification event via the ingestion webhook, expect the following additional fields in the `extras` object:

### 1. Handling Truncated Text
*   **`bigText`**: This field contains the expanded notification body. If the standard `text` field ends in `...`, check `bigText` for the full content.
*   **`titleBig`**: The expanded title, useful if the channel or sender name was cut off.
*   **`textLines`**: For "stacked" notifications, this array contains each individual line.

### 2. Messaging Style & Conversations
Discord notifications typically use `MessagingStyle`. We now extract the full conversation structure:
*   **`messages`**: An array of recent messages in the conversation.
    *   `text`: The message body.
    *   `sender`: The name of the person who sent it.
    *   `time`: The millisecond timestamp.
    *   `uri`: A direct URI to media or the specific message (if available).
*   **`conversationTitle`**: The name of the Group Chat or Channel.
*   **`isGroupConversation`**: Boolean indicating if this is a group/channel vs. a DM.

### 3. Deep Links & Click Targets
*   **`contentIntent`**: A string description of the `PendingIntent`. While the URL is hidden, it often contains class names or routing info.
*   **`link`**, ****`uri`**, **`dataLink`**: The listener searches for common deep link keys in the notification extras. If Discord provides a direct link to the message/channel, it will appear here.

## Bot Integration Strategy

1.  **Prefer `bigText` over `text`**: If `bigText` exists, use it as the primary message body.
2.  **Fallback to `messages` array**: If you need the context of multiple recent messages, iterate through the `messages` array.
3.  **Check for Links**: Scan `extras.link` or `extras.uri` to provide a clickable button in the bot's Discord output that takes the user directly to the source.

---
*Note: These changes were implemented to support better notification mirroring and debugging in the RFD Discord Bot project.*
