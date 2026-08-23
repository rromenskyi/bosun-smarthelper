# Why Bosun

Bosun is built for one specific situation: a boat or an RV, moving,
usually with no reliable internet — a marina Wi-Fi that drops, a Starlink
dish that's slow or metered, a stretch of coastline or backcountry with no
signal at all. Everything on the vessel that's "smart" today falls into
one of two buckets, and neither one actually helps there.

## The two things that already exist, and why neither is enough

**Cloud assistants (Alexa, Siri, a chat app hitting an API)** are
genuinely capable — they can reason, hold a conversation, answer an
open-ended question. But every one of them stops existing the moment the
connection does. On a boat that's most of the time it matters: mid-passage,
at anchor somewhere remote, exactly when something goes wrong and there's
no one else to ask.

**Fixed marine/RV electronics (NMEA displays, tank and battery monitors,
generic checklist apps)** work with no internet at all, which is the right
instinct — but they only ever show a number or a static checklist. They
don't know that "the fresh water tank" and "tank 2" are the same thing you
called two different names on two different dates, don't know what the
manual says about *why* that number matters, and can't be asked a real
question in your own words.

Bosun is built to sit in the gap between those two: reason like the first,
work with nothing like the second.

## What that actually looks like

- **It never goes silent.** `internal/llm.Router` prefers a remote model
  when there's a connection, but a small local model (running on the same
  box, no internet needed) answers everything the remote one would while
  offline — the assistant doesn't degrade to "service unavailable" the
  moment the boat loses signal, which is precisely when a working
  assistant matters most.
- **It actually knows this specific vessel.** Uploaded manuals, past
  memos, and maintenance history are searched by meaning, not exact
  keywords (`docs/memo-search.md`) — "when's the oil due" finds the answer
  whether it was logged as "oil change," "engine service," or a counter
  reading, and a car's odometer and a boat's main-engine hours are the
  same two fields under the hood (`docs/maintenance-tracking.md`). None of
  this requires a connection either — it's all local.
- **It watches the boat even when no one is.** Live sensor data (GPS,
  fridge, system health, and — the moment a real sensor exists — a tank
  level or battery charge, no code change needed) and NOAA weather alerts
  for wherever the boat currently is (`docs/alerts.md`) run continuously
  in the background, not just when someone thinks to ask.

## The killer feature

**It's the only one of the two that can wake you up.**

A cloud assistant can't alert you about anything once you're out of
range — there's no cloud to phone home to. A tank sensor can beep at the
panel, but only if someone's standing next to the panel. Bosun's alerts
run from the same always-on local box the assistant itself lives on, so a
NOAA severe-weather warning for the exact point the boat is at right now,
or a threshold crossing on any monitored metric, gets spoken out loud
through the boat's own speaker (`internal/alerts/speaker.go`) — asleep
belowdecks, zero signal, no phone in hand, and it still reaches you. The
same alert also goes out over Telegram or a webhook the moment there
*is* any connectivity, so it's not an either/or between "offline and
silent" and "online and covered" — it's both, automatically, from one
system that already knows everything else about the boat.

That combination — reasons like a real assistant, remembers and searches
like a real database, and still runs with zero internet, on hardware you
already own — is what nothing else on the market does at once.
