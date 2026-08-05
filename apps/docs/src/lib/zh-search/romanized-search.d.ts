export interface ZhSearchItem {
  title: string;
  id: string;
  group?: string;
  search_aliases?: string[];
}

export interface ZhSearchDocument {
  item: ZhSearchItem;
}

export function normalizeSearch(value: unknown): string;
export function buildSearchIndex(items: ZhSearchItem[]): Promise<ZhSearchDocument[]>;
export function searchIndex(
  index: ZhSearchDocument[],
  rawQuery: string,
  limit?: number,
): Promise<ZhSearchItem[]>;
export function distanceWithinTwo(a: string, b: string): number;
