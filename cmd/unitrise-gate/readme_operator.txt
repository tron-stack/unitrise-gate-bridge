UNITRISE GATE BRIDGE - QUICK START
==================================

What this program does
----------------------
The Gate Bridge keeps your gate system's code list in sync with UnitRise.
When a tenant moves in, pays, falls past due, or moves out, their gate code
is added, suspended, or removed here automatically - the same file-drop
mechanism your gate software already uses.

The icon by the clock
---------------------
The UnitRise hexagon in the system tray is the program's status at a glance:

  AMBER  - healthy and in sync (the menu shows codes and last sync time)
  RED    - needs attention (the menu shows what's wrong)
  GRAY   - the background service isn't running

Click the icon for the menu: Open dashboard, Sync now, and (when one is
published) Install update.

The dashboard
-------------
Click the tray icon and choose "Open dashboard" (or browse to
http://127.0.0.1:47810 on this computer). It shows live status, the sync
activity log, your gate file settings, and is where credentials are entered
or updated. This page is served locally by the agent - it is only reachable
from this computer.

Connecting (pairing)
--------------------
The dashboard asks for three credentials - an access key, an access secret,
and a facility ID. The property operator generates them in the UnitRise
console (their property, then Gate) and can bring them back up any time.
The save folder is the directory your gate software imports codes from
(for PTI systems this is usually C:\PTI).

Starting and stopping
---------------------
The sync runs as the Windows service "UnitRise Gate Bridge" (it starts with
the machine). To stop or start it: Windows search > "Services" > UnitRise
Gate Bridge. Quitting the tray icon never stops the sync.

Updating
--------
When a new version is published, the tray menu and dashboard show a
one-click "Install update" - it downloads, verifies, and restarts the
agent in under a minute. The gate keeps admitting from its current list
throughout.

Removing
--------
Windows Settings > Apps > UnitRise Gate Bridge > Uninstall. The pairing is
kept (reinstalling picks up where it left off), and the last code file
written for your gate software is never touched.

Help
----
Your property's office is your first call - they see this bridge's status
in their UnitRise console. Documentation: https://unitrise.com/help
