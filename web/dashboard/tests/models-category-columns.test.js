import { test } from "node:test";
import assert from "node:assert/strict";
import {
  categoryColumns,
  categoryColspan,
  timeWindowHint,
} from "../src/pages/models/categoryColumns.js";

test("categoryColspan matches each category's rendered columns", () => {
  assert.equal(categoryColspan("all"), 3);
  assert.equal(categoryColspan("text_generation"), 4);
  assert.equal(categoryColspan("embedding"), 3);
  assert.equal(categoryColspan("image"), 3);
  assert.equal(categoryColspan("audio"), 4);
  assert.equal(categoryColspan("video"), 4);
  assert.equal(categoryColspan("utility"), 4);
});

test("unknown categories fall back to the 'all' columns", () => {
  assert.deepEqual(categoryColumns("bogus"), categoryColumns("all"));
});

test("price columns carry the col-price class", () => {
  const [inputOutput] = categoryColumns("all");
  assert.equal(inputOutput.class, "col-price");
  for (const col of categoryColumns("utility")) {
    assert.equal(col.class, "col-price");
  }
});

test("input/output column formats both prices from the pricing arg", () => {
  const [inputOutput] = categoryColumns("text_generation");
  const text = inputOutput.value({}, { input_per_mtok: 1, output_per_mtok: 2 });
  assert.match(text, /\//);
  assert.notEqual(text, "— / —");
  assert.equal(inputOutput.value({}, undefined), "— / —");
});

test("price cells hint at time-of-day pricing windows for their fields", () => {
  const [inputOutput, cached] = categoryColumns("text_generation");
  const pricing = {
    input_per_mtok: 0.44,
    output_per_mtok: 1.32,
    cached_input_per_mtok: 0.014,
    time_windows: [
      {
        label: "off_peak",
        utc_ranges: [
          { days: ["mon", "tue", "wed", "thu", "fri"], start: "10:00", end: "24:00" },
          { days: ["sat", "sun"], start: "00:00", end: "24:00" },
        ],
        pricing: { input_per_mtok: 0.22, output_per_mtok: 0.66 },
      },
    ],
  };
  const hint = inputOutput.hint({}, pricing);
  assert.match(hint, /off peak/);
  assert.match(hint, /\$0\.22 \/ \$0\.66/);
  assert.match(hint, /mon–fri 10:00–24:00, sat,sun 00:00–24:00/);
  assert.match(hint, /UTC/);
  // The window publishes no cached rate, so the cached column stays silent.
  assert.equal(cached.hint({}, pricing), "");
  assert.equal(inputOutput.hint({}, { input_per_mtok: 1 }), "");
  assert.equal(inputOutput.hint({}, undefined), "");
});

test("embedding input column hints at time-of-day pricing windows", () => {
  const [input] = categoryColumns("embedding");
  const pricing = {
    input_per_mtok: 0.1,
    time_windows: [
      {
        label: "off_peak",
        utc_ranges: [{ days: ["sat", "sun"], start: "00:00", end: "24:00" }],
        pricing: { input_per_mtok: 0.05 },
      },
    ],
  };
  assert.equal(input.value({}, pricing), "$0.10");
  const hint = input.hint({}, pricing);
  assert.match(hint, /off peak/);
  assert.match(hint, /\$0\.05/);
  assert.match(hint, /sat,sun 00:00–24:00/);
  assert.equal(input.hint({}, { input_per_mtok: 0.1 }), "");
});

test("timeWindowHint keeps wrapping ranges without days and skips unknown days", () => {
  const hint = timeWindowHint(
    {
      time_windows: [
        {
          label: "night",
          utc_ranges: [{ start: "22:00", end: "06:00" }, { days: ["someday", "Sunday"], start: "00:00", end: "24:00" }],
          pricing: { cached_input_per_mtok: 0.007 },
        },
      ],
    },
    ["cached_input_per_mtok"],
  );
  assert.match(hint, /night: \$0\.01 during 22:00–06:00, sun 00:00–24:00 UTC/);
});

test("timeWindowHint omits ranges and windows the gateway can never apply", () => {
  const pricing = (utc_ranges) => ({
    input_per_mtok: 0.44,
    time_windows: [{ label: "off_peak", utc_ranges, pricing: { input_per_mtok: 0.22 } }],
  });
  // A day list with no recognized weekday matches no day, so it must not be
  // rendered as an all-day schedule.
  assert.equal(
    timeWindowHint(pricing([{ days: ["someday"], start: "00:00", end: "24:00" }, { start: "22:00", end: "06:00" }]), [
      "input_per_mtok",
    ]),
    "off peak: $0.22 during 22:00–06:00 UTC",
  );
  assert.equal(timeWindowHint(pricing([{ days: ["someday"], start: "00:00", end: "24:00" }]), ["input_per_mtok"]), "");
  assert.equal(timeWindowHint(pricing([]), ["input_per_mtok"]), "");
  // Only "mon" or "monday" name a day, never other prefixes such as "monda".
  assert.equal(timeWindowHint(pricing([{ days: ["monda"], start: "10:00", end: "12:00" }]), ["input_per_mtok"]), "");
  assert.match(
    timeWindowHint(pricing([{ days: ["Monday", "MON", "tue"], start: "10:00", end: "12:00" }]), ["input_per_mtok"]),
    /mon,tue 10:00–12:00/,
  );
});

test("timeWindowHint shows the base price for fields a window does not override", () => {
  const [inputOutput] = categoryColumns("all");
  const hint = inputOutput.hint(
    {},
    {
      input_per_mtok: 0.44,
      output_per_mtok: 1.32,
      time_windows: [
        { label: "off_peak", utc_ranges: [{ start: "10:00", end: "01:00" }], pricing: { input_per_mtok: 0.22 } },
      ],
    },
  );
  assert.match(hint, /\$0\.22 \/ \$1\.32/);
});
