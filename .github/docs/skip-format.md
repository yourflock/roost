# Scene Skip Format (.skip)

Scene Skip is an open community standard for crowd-sourcing timestamped content scenes in movies and TV shows. It lets families automatically skip, blur, mute, or get a heads-up about scenes that don't fit their values — without blocking entire titles.

Community scene data is maintained in the [unyeco/roost](https://github.com/unyeco/roost) repository. Any self-hosted Roost instance serves `.skip` data from its own local database, which ships pre-populated from community contributions included in each release.

---

## Sidecar File Format

A `.skip` sidecar is a JSON document. The full JSON Schema is at [`skip-schema.json`](https://github.com/unyeco/roost/blob/main/.github/docs/skip-schema.json).

### Minimal example

```json
{
  "content_id": "imdb:tt1375666",
  "title": "Inception",
  "version": 1,
  "contributors": 42,
  "scenes": [
    {
      "id": "3fa85f64-5717-4562-b3fc-2c963f66afa6",
      "start": 3245,
      "end": 3267,
      "category": "nudity",
      "severity": 2,
      "action": "skip",
      "description": "Brief nudity during dream sequence",
      "votes": 38,
      "disputed": false,
      "confidence": "confirmed"
    }
  ]
}
```

### Top-level fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `content_id` | string | yes | Canonical ID, format: `{source}:{id}`. See [Content IDs](#content-ids) |
| `title` | string | no | Human-readable title (informational) |
| `version` | integer | yes | Schema version. Currently `1` |
| `contributors` | integer | no | Number of unique contributors |
| `generated_at` | ISO 8601 | no | When Roost generated this sidecar |
| `inferred_rating` | string | no | Rating inferred by Roost (G/PG/PG-13/R/NC-17/UNRATED) |
| `scene_summary` | object | no | Count of scenes per category |
| `scenes` | array | yes | Array of scene objects, sorted ascending by `start` |

### Scene fields

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `id` | string | yes | Server-assigned UUID |
| `start` | integer | yes | Seconds from content start |
| `end` | integer | yes | Seconds (exclusive). Must be > `start` |
| `category` | enum | yes | See [Categories](#categories) |
| `severity` | 1–5 | yes | 1=mild, 3=moderate, 5=extreme |
| `action` | enum | yes | See [Actions](#actions) |
| `description` | string | no | Plain-text scene description (max 280 chars) |
| `votes` | integer | no | Net vote count |
| `disputed` | boolean | no | True if heavily downvoted |
| `confidence` | string | no | `confirmed` (votes≥5) or `community_estimate` |

---

## Content IDs

Content IDs use a source prefix:

| Prefix | Format | Example |
|--------|--------|---------|
| `imdb` | `imdb:tt{id}` | `imdb:tt1375666` |
| `tmdb` | `tmdb:movie:{id}` or `tmdb:tv:{id}` | `tmdb:movie:27205` |
| `tvdb` | `tvdb:series:{id}:s{ss}e{ee}` | `tvdb:series:75978:s01e01` |
| `custom` | `custom:{slug}` | `custom:my-home-video` |

Roost normalizes all IDs to the canonical form on submission. Content without a recognized ID cannot have a `.skip` sidecar.

---

## Categories

| Category | Description |
|----------|-------------|
| `sex` | Sexual content or acts |
| `nudity` | Non-sexual nudity |
| `kissing` | Kissing or mild romance |
| `romance` | Romantic scenes (no physical contact) |
| `violence` | Physical violence, fighting |
| `gore` | Graphic injury, blood, body horror |
| `language` | Profanity, crude language |
| `drugs` | Drug use, alcohol, smoking |
| `jump_scare` | Sudden loud/visual scare |
| `scary` | Generally frightening content (without jump scare) |

---

## Actions

| Action | What Owl does |
|--------|---------------|
| `skip` | Seek video to `end` timestamp instantly |
| `blur` | Mosaic overlay for the scene duration |
| `mute` | Audio muted for scene duration |
| `warn` | Playback pauses; user chooses Skip or Play |

These are *recommended* defaults. Each profile can override the action per category.

---

## Profile Default Policies

Owl applies these defaults when a profile is created. Families can customize per-profile.

| Profile Type | Default Behavior |
|-------------|-----------------|
| Kids (under 13) | Skip: sex, nudity, gore, drugs. Mute: language. Warn: jump_scare |
| Teen (13–17) | Skip: sex/nudity severity≥3. Warn: gore, drugs |
| Adult | Off (all scenes play, manual overrides only) |
| Family | Skip: sex, nudity. Warn: gore |

---

## API

### Fetch sidecar (public, no auth)

```http
GET https://{your-roost-instance}/skip/v1/{content_id}
```

Returns the `.skip` sidecar JSON for a content ID. Approved scenes only (`votes >= 5`). Cached 1 hour in Redis.

### Submit a scene

```http
POST https://{your-roost-instance}/skip/v1/scenes
Authorization: Bearer {api_token}
Content-Type: application/json

{
  "content_id": "imdb:tt1375666",
  "start": 3245,
  "end": 3267,
  "category": "nudity",
  "severity": 2,
  "action": "skip",
  "description": "Brief nudity during dream sequence"
}
```

Requires a Roost API token (from Settings > API Tokens).

### Vote on a scene

```http
POST https://{your-roost-instance}/skip/v1/scenes/{id}/vote
Authorization: Bearer {api_token}

{ "vote": 1 }   // 1 = upvote, -1 = downvote
```

One vote per user per scene. Scenes auto-approve at net vote count ≥ 5. Scenes are marked disputed when downvotes exceed upvotes by more than 3.

---

## Contributing

Scene data is community-maintained. After you watch a movie or episode in Owl, you'll see a prompt: "Did you notice any scenes to flag?" Tap Yes to open the contribution UI.

You can also contribute directly via the API. The spec is MIT-licensed and we welcome PRs to the [unyeco/roost](https://github.com/unyeco/roost) repository.

---

## Self-hosted Roost

If you run your own Roost instance, `.skip` files ship with Roost updates from the community-maintained database. Community submissions are reviewed and merged via pull requests to [unyeco/roost](https://github.com/unyeco/roost) and distributed with each release.
