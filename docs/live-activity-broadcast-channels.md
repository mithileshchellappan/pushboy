# Live Activity Broadcast Channels

Pushboy supports APNs broadcast channels as an additional Live Activity
delivery cohort. It does not replace the existing per-activity APNs and FCM
token path.

An individual Live Activity uses either `PushType.channel(channelId)` or
`PushType.token`; it never subscribes to both. A shared event can still have
both cohorts across different devices:

- channel-mode activities receive one APNs broadcast;
- token-mode activities receive the existing per-token fanout.

Starts are always sent to device-specific APNs push-to-start tokens. A channel
only selects how the resulting activity receives later updates and its end
event.

## Requirements

Before using broadcast channels:

1. Enable Push Notifications and Live Activities for the app.
2. Enable Apple's Broadcast Capability for the App ID and regenerate affected
   provisioning profiles.
3. Use iOS or iPadOS 18 or later for channel-capable builds.
4. Configure Pushboy with the same APNs key, team ID, bundle ID, and
   sandbox/production environment used by the client.

No separate feature flag is required. Channel delivery is selected by
provisioning an activity mapping and registering channel-capable start tokens.

Channel IDs are opaque and environment-specific. Run separate Pushboy
deployments for sandbox and production; do not mix their credentials, tokens,
or channels.

Apple documents the platform requirements in
[Setting up broadcast push notifications](https://developer.apple.com/documentation/usernotifications/setting-up-broadcast-push-notifications).

## What the backend provides

Pushboy owns channel creation. The product backend supplies:

- `activityId`: the stable product identifier for one event;
- `topicId`: the Pushboy topic whose audience follows that event.

Channel delivery applies to topic-scoped Live Activity jobs whose `topicId`
matches this mapping. User-scoped jobs keep the existing direct-token path.

Provision one channel for the pair before starting the event:

```http
PUT /v1/live-activity/channels/match-2026-final
Content-Type: application/json

{
  "topicId": "football"
}
```

Pushboy creates an APNs channel with the fixed `no-storage` policy and stores
only the completed mapping:

```json
{
  "activityId": "match-2026-final",
  "topicId": "football",
  "channelId": "<opaque APNs channel id>",
  "createdAt": "2026-07-23T12:00:00Z"
}
```

Once Pushboy has stored the completed mapping, repeating the same `activityId`
and `topicId` is idempotent. APNs channel creation has no idempotency key, so
Pushboy never retries the remote APNs create call after an error. After APNs
returns a channel ID, Pushboy retries an ambiguous local mapping write once
through the idempotent mapping operation. Reusing the `activityId` with another
topic returns `409`.

The management endpoints are:

- `PUT /v1/live-activity/channels/{activityID}` to create or return a mapping;
- `GET /v1/live-activity/channels/{activityID}` to read it;
- `DELETE /v1/live-activity/channels/{activityID}` to delete it at APNs and
  remove the mapping.

Call `DELETE` after the event is finished and no later update will use the
channel. Pushboy does not run a separate retirement state machine or reaper.

These are trusted backend operations. The mobile client does not create an APNs
channel or upload a channel ID to Pushboy.

## What the client gives Pushboy

The client continues to upload its APNs push-to-start token. A build that can
consume broadcast channels also sends `supportsBroadcastChannels: true`:

```http
POST /v1/live-activity/tokens
Content-Type: application/json

{
  "userId": "user-123",
  "topicId": "football",
  "platform": "apns",
  "tokenType": "start",
  "token": "<push-to-start-token>",
  "supportsBroadcastChannels": true
}
```

Set the capability to true only when all of these are true:

- the runtime supports `PushType.channel`;
- Broadcast Capability is in the app's provisioning profile;
- the released app code handles channel-mode starts;
- the feature is enabled for that app build.

The flag applies only to APNs start tokens. Existing clients can omit it and
keep their current token behavior.

The client also keeps the existing token flow for token-mode activities:

- observe every push-to-start token rotation and replace the registered token;
- observe `pushTokenUpdates` for token-mode activities and upload the update
  token with the product `activityId`;
- invalidate old update tokens when they rotate or the activity ends.

Channel-mode activities do not produce an individual update token.

## How remote start works

For every audience member, Pushboy sends the normal direct APNs start. The
registered capability and channel mapping only change the start payload:

| Client and mapping | Start payload | Later delivery |
| --- | --- | --- |
| Channel-capable and matching topic mapping exists | `input-push-channel: "<channelId>"` | APNs channel |
| Channel-capable and no mapping exists | `input-push-token: 1` | Per-activity token |
| Capability absent or false | Neither field | Existing legacy behavior |

Both input fields live inside the start payload's `aps` dictionary. Do not send
`input-push-channel` to an older client.

The client should observe `Activity<Attributes>.activityUpdates` so it can
discover a remotely started activity. If that activity is in token mode,
observe and upload its update token:

```swift
Task {
    for await activity in Activity<MatchAttributes>.activityUpdates {
        Task {
            for await tokenData in activity.pushTokenUpdates {
                let token = tokenData.map {
                    String(format: "%02x", $0)
                }.joined()

                await tokenManager.replaceUpdateToken(
                    activityId: activity.attributes.activityId,
                    token: token
                )
            }
        }
    }
}
```

The attributes should contain the stable product `activityId`; ActivityKit's
local `Activity.id` is different on each device.

An APNs success for the direct start proves provider acceptance, not that the
device created and rendered the activity. Use device-side observation or
telemetry to validate that boundary.

## Updates and end events

The product backend uses the existing Live Activity job endpoint with the
stable `activityId`. For an update or end, Pushboy:

1. publishes once to the matching APNs channel for a topic-scoped job, if
   present; and
2. runs the existing per-token APNs and FCM fanout.

The channel publish is an ordinary Live Activity send in the same dispatch and
outcome pipeline. Channel failures do not change or invalidate token records,
and token failures do not remove the channel mapping.

Pushboy always uses Apple's `no-storage` channel policy and sends broadcast
expiration `0`. A missed broadcast is not replayed, so each update should be a
complete state snapshot rather than a delta. Include the current state in the
start payload as well; an immediate update can arrive before a newly started
device has subscribed.

Apple documents the provider requests in
[Sending channel management requests to APNs](https://developer.apple.com/documentation/usernotifications/sending-channel-management-requests-to-apns)
and
[Sending broadcast push notification requests to APNs](https://developer.apple.com/documentation/usernotifications/sending-broadcast-push-notification-requests-to-apns).

## Validation checklist

Test the same event with:

- a legacy client, which keeps direct start and token updates;
- a channel-capable client with a mapping, which gets
  `input-push-channel`;
- a channel-capable client without a mapping, which gets
  `input-push-token`;
- a mixed topic, where one update reaches the channel cohort and the existing
  token cohort;
- update and end payloads through both paths;
- channel deletion and a later `404` from the mapping API;
- separate sandbox and production deployments.

Keep these boundaries distinct while debugging: Pushboy queued the send, APNs
accepted it, the device received it, and ActivityKit rendered it. The first two
do not prove the last two.
