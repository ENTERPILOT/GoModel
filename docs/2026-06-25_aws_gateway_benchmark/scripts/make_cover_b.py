#!/usr/bin/env python3
"""Cover for the measured benchmark post variant (B).

Same hero visual as make_cover.py (the resource gap: Docker image + peak RAM,
GoModel highlighted). The text is a single takeaway -
"Four gateways, one backend - GoModel wins" - with no cost-question framing.
"""
import sys
import matplotlib
matplotlib.use("Agg")
import matplotlib.pyplot as plt
from matplotlib import font_manager as fm

BG = "#0b0e14"
PANEL = "#11161f"
TEXT = "#e6edf3"
MUTED = "#8b98a9"
GREEN = "#34d399"   # GoModel
RED = "#f87171"     # LiteLLM
GRAY = "#5b6675"    # others

def font(weight="normal", size=12, black=False):
    fam = "Arial Black" if black else "Arial"
    return fm.FontProperties(family=fam, weight=weight, size=size)

# data (June 2026 c7i.large run) - ascending, GoModel (winner) on top.
IMG = [("GoModel", 16, GREEN), ("Portkey", 59, GRAY), ("Bifrost", 77, GRAY), ("LiteLLM", 372, RED)]
RAM = [("GoModel", 37, GREEN), ("Portkey", 112, GRAY), ("Bifrost", 143, GRAY), ("LiteLLM", 2272, RED)]

W, H, DPI = 2400, 1260, 200
fig = plt.figure(figsize=(W / DPI, H / DPI), dpi=DPI)
fig.patch.set_facecolor(BG)

# ── left text column (single takeaway, no cost-question headline) ───
T = dict(va="top", ha="left")
fig.text(0.045, 0.93, "AI GATEWAY BENCHMARK  ·  JUNE 25, 2026", color=GREEN,
         fontproperties=font(size=14.5, weight="bold"), **T)
fig.text(0.043, 0.72, "Four gateways,", color=TEXT, fontproperties=font(size=33, black=True), **T)
fig.text(0.043, 0.60, "one backend —", color=TEXT, fontproperties=font(size=33, black=True), **T)
fig.text(0.043, 0.48, "GoModel wins", color=GREEN, fontproperties=font(size=33, black=True), **T)

def panel(rect, title, rows, unit, ref):
    ax = fig.add_axes(rect)
    ax.set_facecolor(PANEL)
    for s in ax.spines.values():
        s.set_visible(False)
    ax.tick_params(left=False, bottom=False, labelbottom=False)
    labels = [r[0] for r in rows]
    vals = [r[1] for r in rows]
    colors = [r[2] for r in rows]
    y = range(len(rows))
    maxv = max(vals)
    ax.barh(y, vals, color=colors, height=0.62, zorder=3)
    ax.set_xlim(0, maxv * 1.34)
    ax.set_ylim(-0.6, len(rows) - 0.4)
    ax.invert_yaxis()
    ax.set_yticks(list(y))
    ax.set_yticklabels(labels, color=TEXT, fontproperties=font(size=14, weight="bold"))
    for i, v in enumerate(vals):
        mult = v / ref
        tag = "1×" if abs(mult - 1) < 0.05 else f"{mult:.0f}×"
        label = f"{v:,} {unit}   ({tag})"
        if colors[i] == RED:  # the worst: label centered inside the bar, dark text
            ax.text(v / 2, i, label, va="center", ha="center", color=BG,
                    fontproperties=font(size=12.5, weight="bold"))
        else:
            ax.text(v + maxv * 0.02, i, label, va="center", ha="left",
                    color=TEXT if colors[i] != GRAY else MUTED,
                    fontproperties=font(size=12.5, weight="bold"))
    ax.set_title(title, loc="left", color=MUTED, fontproperties=font(size=14, weight="bold"), pad=8)
    return ax

panel([0.55, 0.575, 0.36, 0.295], "DOCKER IMAGE (COMPRESSED)", IMG, "MB", 16)
panel([0.55, 0.135, 0.36, 0.295], "PEAK RAM UNDER LOAD", RAM, "MB", 37)

out = sys.argv[1] if len(sys.argv) > 1 else "cover-b.png"
fig.savefig(out, facecolor=BG, dpi=DPI)
print("wrote", out)
