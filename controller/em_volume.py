"""Volume scale conversion between the device's native level and HA's float.

Split out as pure logic for the reason em_linkauth.py was: the three call
sites (em_controller, em_esphome, em_api) each had their OWN copy of
`level / 175`, so the scale lived in three places and none of them were
covered by a test. Changing the ceiling meant finding all three.

The device level is the raw tinymix ctl 61 index — the tlv320aic32x4 DAC
digital volume, 0.5dB per step with 0dB (unity) at index 127. The scale is
therefore dB-linear, which is roughly perceptually linear, so the mapping to
HA's 0.0–1.0 is a plain proportion and needs no extra taper.

DEVICE_VOLUME_MAX is 127 and NOT the control's own maximum of 175. Indexes
above 127 apply positive digital gain to near-full-scale PCM and saturate
inside the DAC — measured at 65% THD by index 153 (see
device/internal/server/volume.go for the full measurement and why stock
FireOS never touches this control).
"""

# Codec unity gain. The mixer control accepts up to 175; everything above
# this clips. See the module docstring.
DEVICE_VOLUME_MAX = 127

# 0.5dB per index step, 0dB at DEVICE_VOLUME_MAX.
DB_PER_STEP = 0.5


def level_to_db(level: int) -> float:
    """Device level as dB relative to unity. Index 127 -> 0.0, index 0 -> -63.5."""
    return (level - DEVICE_VOLUME_MAX) * DB_PER_STEP


def device_level_to_ha(level: int) -> float:
    """Convert a device volume level to an HA float (0.0–1.0)."""
    try:
        return max(0.0, min(1.0, float(level) / DEVICE_VOLUME_MAX))
    except (TypeError, ValueError, ZeroDivisionError):
        return 0.0


def ha_volume_to_device(volume: float) -> int:
    """Convert an HA volume float (0.0–1.0) to a device volume level.

    Clamped to the codec's unity gain, so HA asking for full volume can never
    put the DAC into positive digital gain.
    """
    return max(0, min(DEVICE_VOLUME_MAX, round(float(volume) * DEVICE_VOLUME_MAX)))


# ─── The stock-sound profile's volume law (#229) ──────────────────────────────
#
# Stock FireOS maps its 31 volume steps through this table (dB), the same for
# music, speech and alarms, and applies it BEFORE the driver correction and
# compressors, so they work hard only at high volume. A device level's
# percentage lands on the table by linear interpolation (level 127 = step 30).
# Above 70% stock also raises the level into its compressors: +3.5dB to 80%,
# +7.5dB to 90%, +14.5dB to 100%, relative to the curve below 70%, which
# itself sits 0.8dB under unity at 1kHz. Read off a stock Dot's /system; the
# numbers are a fact about how it behaves, not its code.
STOCK_VOLUME_TABLE_DB = (-90, -33, -30, -26, -25, -23, -21, -19, -17, -16, -14, -13, -12,
                         -10, -9, -8, -7, -5, -5, -4, -4, -4, -4, -3, -3, -3, -3, -3,
                         -2, -1, 0)
STOCK_LEVEL_STEPS_DB = ((70, 0.0), (80, 3.5), (90, 7.5), (100, 14.5))
STOCK_BASE_DB = -0.8


def stock_level_db(level: int) -> float | None:
    """Gain in dB for a device level under the stock-sound profile, or None
    for silence (level 0)."""
    if level <= 0:
        return None
    level = min(level, DEVICE_VOLUME_MAX)
    pct = level * 100.0 / DEVICE_VOLUME_MAX
    pos = pct * (len(STOCK_VOLUME_TABLE_DB) - 1) / 100.0
    i = min(int(pos), len(STOCK_VOLUME_TABLE_DB) - 2)
    frac = pos - i
    table = STOCK_VOLUME_TABLE_DB[i] + frac * (STOCK_VOLUME_TABLE_DB[i + 1] - STOCK_VOLUME_TABLE_DB[i])
    step = next(db for edge, db in STOCK_LEVEL_STEPS_DB if pct <= edge + 1e-9)
    return table + step + STOCK_BASE_DB
