import json
import math
import os
import random
from statistics import mean
from typing import List, Tuple

# ---------- Tunables ----------
SEED = 42  # set to None for non-deterministic
N_TRUCKS = 100
N_STEPS = 100

# Motion path (simple parametric “road” used to place trucks)
STEP_SPACING = 100.0  # distance between successive road samples along X
LANE_SPACING = 4.0  # Z offset per truck (visual lanes)

# Timing
BASE_DT = 0.5  # nominal seconds between keyframes (pre-jitter, pre-speed)
MIN_DT = 0.15  # hard floor to keep times strictly increasing
START_DELAY_RANGE = (0.0, 40.0)
SPEED_MULT_RANGE = (0.6, 1.6)  # >1.0 => faster (smaller dt)
JITTER_STD = 0.06
TIME_SCALE = 1.0  # animation.time_scale (sim seconds per real second)

# Output
ANIMATION_NAME = "Simulation 1"
OUT_FILE = "demo_animation.json"
# --------------------------------

if SEED is not None:
    random.seed(SEED)

print("Saving to:", os.getcwd())


def road_point(i: int) -> Tuple[float, float, float]:
    """
    Parametric 'road': advance along +X, slight sinusoid on Z, y=0.
    You can replace this with your real sampler if needed.
    """
    x = i * STEP_SPACING
    y = 0.0
    z = 5.0 * math.sin(i / 2.0)
    return x, y, z


def randomized_times(n_steps: int) -> List[float]:
    """
    Strictly-increasing times with start delay, speed scaling and jitter.
    dt_i = max(MIN_DT, (BASE_DT / speed_mult) + jitter)
    """
    start_delay = random.uniform(*START_DELAY_RANGE)
    speed_mult = random.uniform(*SPEED_MULT_RANGE)

    times = []
    t = start_delay
    for _ in range(n_steps):
        nominal = BASE_DT / max(1e-9, speed_mult)
        jitter = random.gauss(0.0, JITTER_STD)
        dt = max(MIN_DT, nominal + jitter)
        t += dt
        times.append(round(t, 3))
    return times


def yaw_deg(p_prev, p_curr) -> float:
    """
    Yaw in degrees around Y axis, derived from horizontal (X,Z) motion.
    0° = facing +X, +90° = facing +Z.
    """
    dx = p_curr[0] - p_prev[0]
    dz = p_curr[2] - p_prev[2]
    # atan2(x,z) so that +X is 0°, +Z is +90°
    ang = math.degrees(math.atan2(dx, dz))
    return ang


# ---------------- Build objects & transitions ----------------
objects = []
transition = []
durations = []

for n in range(N_TRUCKS):
    truck_id = n + 1
    times = randomized_times(N_STEPS)

    # Build the positional keyframes along our synthetic road, lane-shifted in Z
    positions = []
    for i in range(N_STEPS):
        x, y, z = road_point(i)
        z += (n - 1) * LANE_SPACING
        positions.append((x, y, z))

    # Initial pose from first keyframe
    x0, y0, z0 = positions[0]
    # For initial yaw, peek the next position if available, else default 0°
    if N_STEPS > 1:
        yaw0 = yaw_deg(positions[0], positions[1])
    else:
        yaw0 = 0.0

    objects.append(
        {
            "id": truck_id,
            "type": "container_truck",
            "x": round(x0, 3),
            "y": round(y0, 3),
            "z": round(z0, 3),
            "rotation_x": 0.0,
            "rotation_y": round(yaw0, 3),
            "rotation_z": 0.0,
            "color": "black",
        }
    )

    # Pair each time with both move & rotation transitions
    # Also compute per-track duration stats
    durations.append(times[-1] - times[0])

    for i, t in enumerate(times):
        px, py, pz = positions[i]

        # Movement keyframe
        transition.append(
            {
                "time": t,
                "objId": truck_id,
                "type": "move",
                "x": round(px, 3),
                "y": round(py, 3),
                "z": round(pz, 3),
            }
        )

        # Rotation keyframe (yaw from local motion direction)
        if i == 0 and len(positions) > 1:
            yaw = yaw_deg(positions[0], positions[1])
        elif i > 0:
            yaw = yaw_deg(positions[i - 1], positions[i])
        else:
            yaw = 0.0

        transition.append(
            {
                "time": t,
                "objId": truck_id,
                "type": "rotation",
                "x": 0.0,
                "y": round(yaw, 3),
                "z": 0.0,
            }
        )

# --------------- Compose final JSON ---------------
payload = {
    "animation": {"name": ANIMATION_NAME, "time_scale": TIME_SCALE},
    "objects": objects,
    "transition": transition,
}

out_path = os.path.join(os.getcwd(), OUT_FILE)
with open(out_path, "w", encoding="utf-8") as f:
    json.dump(payload, f, indent=2)

print("Wrote:", out_path)
print(
    f"Trucks: {N_TRUCKS} | Steps per truck: {N_STEPS} | Transitions total: {len(transition)}"
)
print(
    f"Per-track durations (s) — min: {min(durations):.2f}, "
    f"mean: {mean(durations):.2f}, max: {max(durations):.2f}"
)
