const BASE64 = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
const DIGITS = new Map([...BASE64].map((character, index) => [character, index]));
const CJK_FIRST = 0x4e00;
const VARIANT_LIMIT = 16;

let tablesPromise;

function decode(encoded) {
  const values = [];
  let value = 0;
  let shift = 0;
  for (const character of encoded) {
    const digit = DIGITS.get(character);
    if (digit === undefined) continue;
    value |= (digit & 31) << shift;
    if (digit & 32) {
      shift += 5;
      continue;
    }
    values.push(value);
    value = 0;
    shift = 0;
  }
  return values;
}

function decodeRunning(encoded) {
  let running = 0;
  return decode(encoded).map((delta) => (running += delta));
}

function invert(source) {
  const table = new Map();
  for (const [reading, encoded] of Object.entries(source)) {
    for (const code of decodeRunning(encoded)) {
      const existing = table.get(code);
      if (existing) existing.push(reading);
      else table.set(code, [reading]);
    }
  }
  return table;
}

function tables() {
  if (!tablesPromise) {
    tablesPromise = import("../data/romanize.js").then(({ PINYIN, JYUTPING, SIMPLIFY }) => {
      const codes = decodeRunning(SIMPLIFY[0]);
      const targets = decode(SIMPLIFY[1]);
      const simplify = new Map();
      codes.forEach((code, index) => simplify.set(code, targets[index] + CJK_FIRST));
      return { pinyin: invert(PINYIN), jyutping: invert(JYUTPING), simplify };
    });
  }
  return tablesPromise;
}

export function normalizeSearch(value) {
  return String(value ?? "")
    .normalize("NFKC")
    .toLocaleLowerCase("zh-Hans")
    .replace(/[̀-ͯ]/g, "")
    .replace(/[^\p{L}\p{N}]+/gu, " ")
    .trim();
}

function compact(value) {
  return normalizeSearch(value).replaceAll(" ", "");
}

function readingsOf(character, table, simplify) {
  const code = character.codePointAt(0);
  return table.get(code) ?? table.get(simplify.get(code) ?? -1) ?? null;
}

function romanize(text, table, simplify) {
  const groups = [];
  let latin = "";
  const flush = () => {
    if (!latin) return;
    groups.push({ readings: [latin], han: false });
    latin = "";
  };
  for (const character of text) {
    const readings = readingsOf(character, table, simplify);
    if (readings) {
      flush();
      groups.push({ readings, han: true });
      continue;
    }
    if (/\s/u.test(character)) {
      flush();
      continue;
    }
    latin += character;
  }
  flush();
  return groups;
}

function expand(groups, pick) {
  let variants = [[]];
  for (const { readings } of groups) {
    const next = [];
    for (const prefix of variants) {
      for (const reading of readings) {
        next.push([...prefix, pick(reading)]);
        if (next.length >= VARIANT_LIMIT) break;
      }
      if (next.length >= VARIANT_LIMIT) break;
    }
    variants = next;
  }
  return variants;
}

function whole(reading) {
  return reading;
}

function initial(reading) {
  return reading[0] ?? "";
}

function simplifyText(text, simplify) {
  let out = "";
  for (const character of text) {
    const mapped = simplify.get(character.codePointAt(0));
    out += mapped === undefined ? character : String.fromCodePoint(mapped);
  }
  return out;
}

function romanizedForms(text, table, simplify) {
  const groups = romanize(text, table, simplify);
  const han = groups.filter((group) => group.han);
  const spaced = expand(groups, whole).map((parts) => normalizeSearch(parts.join(" ")));
  const joined = expand(groups, whole).map((parts) => parts.join("").toLowerCase());
  const hanJoined = han.length > 0 && han.length < groups.length
    ? expand(han, whole).map((parts) => parts.join("").toLowerCase())
    : [];
  const initials = [
    ...expand(groups, initial).map((parts) => parts.join("").toLowerCase()),
    ...(han.length > 0 ? expand(han, initial).map((parts) => parts.join("").toLowerCase()) : []),
  ];
  return { spaced, joined: [...joined, ...hanJoined], initials };
}

export async function buildSearchIndex(items) {
  const { pinyin, jyutping, simplify } = await tables();
  return items.map((item) => {
    const source = [item.title, item.id, item.group, ...(item.search_aliases ?? [])]
      .filter(Boolean)
      .join(" ");
    const text = normalizeSearch(source);
    const py = romanizedForms(source, pinyin, simplify);
    const jy = romanizedForms(source, jyutping, simplify);
    return {
      item,
      text,
      simplified: normalizeSearch(simplifyText(source, simplify)),
      compact: compact(source),
      simplifiedCompact: compact(simplifyText(source, simplify)),
      spaced: [...py.spaced, ...jy.spaced],
      joined: [...py.joined, ...jy.joined],
      initials: [...py.initials, ...jy.initials],
    };
  });
}

// Transposition reads row i-2, so this keeps three rows, not two.
export function distanceWithinTwo(a, b) {
  if (!a || !b || Math.abs(a.length - b.length) > 2) return 3;
  let older = null;
  let previous = Array.from({ length: b.length + 1 }, (_, index) => index);
  for (let i = 1; i <= a.length; i += 1) {
    const current = new Array(b.length + 1);
    current[0] = i;
    let rowMin = current[0];
    for (let j = 1; j <= b.length; j += 1) {
      const cost = a[i - 1] === b[j - 1] ? 0 : 1;
      let next = Math.min(current[j - 1] + 1, previous[j] + 1, previous[j - 1] + cost);
      if (i > 1 && j > 1 && a[i - 1] === b[j - 2] && a[i - 2] === b[j - 1]) {
        next = Math.min(next, older[j - 2] + 1);
      }
      current[j] = next;
      if (next < rowMin) rowMin = next;
    }
    if (rowMin > 2) return 3;
    older = previous;
    previous = current;
  }
  return Math.min(previous[b.length], 3);
}

function scoreDocument(document, query, queryCompact, queryRomanized) {
  if (!query) return 1;
  const fields = [document.text, document.simplified, ...document.spaced];
  const compactFields = [
    document.compact,
    document.simplifiedCompact,
    ...document.joined,
  ];
  if (fields.some((field) => field === query) || compactFields.some((field) => field === queryCompact)) return 100;
  if (fields.some((field) => field.startsWith(query)) || compactFields.some((field) => field.startsWith(queryCompact))) return 82;
  if (fields.some((field) => field.split(" ").some((token) => token.startsWith(query)))) return 72;
  if (compactFields.some((field) => field.includes(queryCompact))) return 60;
  if (document.initials.some((field) => field.startsWith(queryCompact))) return 56;
  if (queryRomanized.length > 0 && compactFields.some((field) => queryRomanized.some((form) => field.includes(form)))) return 54;
  if (queryCompact.length >= 4) {
    const tokens = [...fields.flatMap((field) => field.split(" ")), ...compactFields];
    const best = Math.min(...tokens.map((token) => distanceWithinTwo(queryCompact, token)));
    if (best <= 2) return 42 - best * 8;
  }
  return 0;
}

export async function searchIndex(index, rawQuery, limit = 50) {
  const query = normalizeSearch(rawQuery);
  if (!query) return index.slice(0, limit).map(({ item }) => item);
  const { pinyin, jyutping, simplify } = await tables();
  const queryCompact = compact(rawQuery);
  const queryRomanized = [
    ...romanizedForms(rawQuery, pinyin, simplify).joined,
    ...romanizedForms(rawQuery, jyutping, simplify).joined,
  ].filter((form) => form && form !== queryCompact);

  return index
    .map((document) => ({
      item: document.item,
      score: scoreDocument(document, query, queryCompact, queryRomanized),
    }))
    .filter(({ score }) => score > 0)
    .sort((a, b) => b.score - a.score || String(a.item.title).localeCompare(String(b.item.title), "zh-Hans"))
    .slice(0, limit)
    .map(({ item }) => item);
}
