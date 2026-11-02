# Closing owed items

As of 26 October 2026.

Only the user could close an owed item, with `/done N`, and `/done` was
missing from the page's `/` menu. In one chat, items the user had said were
done stayed open for days, so the list grew only because closing was hard.

Changes:

- `owe(close, from)` closes an item for Opt. `from` must be a user's
  message after the one that asked for it: the message that confirms the
  item done. Opt asks first. The tool's answer stays in the log.
- `owe(item, state)` tags an item `mine`, `waiting` (on the user) or
  `parked` (put off by the user). The list that opens each turn shows Opt's
  own items in full and cuts the others to 80 bytes.
- `/owed` and `/done` are in the `/` menu.

There is no cap on the list. Items leave it as they close, and a cap would
hide work that is still owed.
