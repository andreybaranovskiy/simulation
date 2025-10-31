import json
import math
import os
import random
from statistics import mean

# ---------- Tunables ----------
SEED = 42  # set to None for non-deterministic
N_ROAD_POINTS = 1000
N_TRUCKS = 1000
N_STEPS = 100

BASE_DT = 0.5  # nominal seconds between keyframes (before speed/jitter)
MIN_DT = 0.15  # hard floor to keep times strictly increasing
START_DELAY_RANGE = (0.0, 40.0)  # per-truck start delay [s]
SPEED_MULT_RANGE = (
    0.6,
    1.6,
)  # per-truck speed multiplier (higher = slower time gaps if <1? see below)
JITTER_STD = 0.06  # per-step Gaussian noise [s]
# --------------------------------

if SEED is not None:
    random.seed(SEED)

print("Saving to:", os.getcwd())  # shows current working directory

# Road points (same geometry as your snippet)
road = [{"x": i * 100, "y": 0, "z": 5 * math.sin(i / 2)} for i in range(N_ROAD_POINTS)]


def randomized_times(n_steps: int) -> list[float]:
    """
    Build a strictly-increasing time series with per-track start delay, speed scaling and jitter.
    We compute dt_i = max(MIN_DT, (BASE_DT / speed_mult) + jitter), then cumulative-sum.
    Note: speed_mult > 1.0 => faster (smaller dt); < 1.0 => slower (larger dt).
    """
    start_delay = random.uniform(*START_DELAY_RANGE)
    speed_mult = random.uniform(*SPEED_MULT_RANGE)

    times = []
    t = start_delay
    for _ in range(n_steps):
        # Gaussian jitter around the scaled base
        nominal = BASE_DT / speed_mult
        jitter = random.gauss(0.0, JITTER_STD)
        dt = max(MIN_DT, nominal + jitter)
        t += dt
        times.append(round(t, 3))  # 1–3 ms precision is plenty
    return times


tracks = []
durations = []

for n in range(N_TRUCKS):
    times = randomized_times(N_STEPS)
    durations.append(times[-1] - times[0])

    keyframes = [
        {"t": times[i], "x": i * 100, "y": 0, "z": 5 * math.sin(i / 2) + (n - 1) * 4}
        for i in range(N_STEPS)
    ]
    tracks.append({"id": f"Truck {n+1}", "keyframes": keyframes})

out_path = os.path.join(os.getcwd(), "demo_road_tracks.json")
with open(out_path, "w", encoding="utf-8") as f:
    json.dump({"road": {"width": 8, "points": road}, "tracks": tracks}, f, indent=2)

print("Wrote:", out_path)
print(f"Tracks: {N_TRUCKS} | Steps per track: {N_STEPS}")
print(
    f"Per-track durations (s) — min: {min(durations):.2f}, "
    f"mean: {mean(durations):.2f}, max: {max(durations):.2f}"
)
