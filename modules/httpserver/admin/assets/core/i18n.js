const STORAGE_KEY = "kiln.admin.locale";
const DEFAULT_LOCALE = "zh-Hans";

export const LOCALE_OPTIONS = [
  { value: "zh-Hans", label: "简体中文" },
  { value: "zh-Hant", label: "繁體中文" },
  { value: "en", label: "English" },
];

const messages = {
  "zh-Hans": {
    "meta.loginTitle": "登录 · Kiln",
    "shared.skipToContent": "跳到主要内容",
    "login.language": "语言",
    "login.eyebrow": "KILN 管理",
    "login.title": "登录管理控制台",
    "login.description": "管理频道、访问权限、网络出口与系统设置。",
    "login.username": "用户名",
    "login.password": "密码",
    "login.remember": "保持登录状态",
    "login.submit": "登录",
    "login.submitting": "正在登录…",
    "login.error.usernameRequired": "请输入用户名。",
    "login.error.passwordRequired": "请输入密码。",
    "login.error.invalidCredentials": "用户名或密码不正确。",
    "login.error.adminRequired": "此账号无权访问管理控制台。",
    "login.error.rateLimited": "登录尝试次数过多，请稍后再试。",
    "login.error.invalidRequest": "登录信息格式不正确，请检查后重试。",
    "login.error.accessDenied": "登录请求被拒绝，请联系管理员。",
    "login.error.serviceUnavailable": "登录服务暂时不可用，请稍后重试。",
    "login.error.serverError": "服务器暂时无法完成登录，请稍后重试。",
    "login.error.networkError": "无法连接服务器，请检查网络后重试。",
    "login.error.unknown": "登录失败，请稍后重试。",
  },
  "zh-Hant": {
    "meta.loginTitle": "登入 · Kiln",
    "shared.skipToContent": "跳至主要內容",
    "login.language": "語言",
    "login.eyebrow": "KILN 管理",
    "login.title": "登入管理控制台",
    "login.description": "管理頻道、存取權限、網路出口與系統設定。",
    "login.username": "使用者名稱",
    "login.password": "密碼",
    "login.remember": "保持登入狀態",
    "login.submit": "登入",
    "login.submitting": "正在登入…",
    "login.error.usernameRequired": "請輸入使用者名稱。",
    "login.error.passwordRequired": "請輸入密碼。",
    "login.error.invalidCredentials": "使用者名稱或密碼不正確。",
    "login.error.adminRequired": "此帳號無權存取管理控制台。",
    "login.error.rateLimited": "登入嘗試次數過多，請稍後再試。",
    "login.error.invalidRequest": "登入資訊格式不正確，請檢查後重試。",
    "login.error.accessDenied": "登入請求遭到拒絕，請聯絡管理員。",
    "login.error.serviceUnavailable": "登入服務暫時無法使用，請稍後再試。",
    "login.error.serverError": "伺服器暫時無法完成登入，請稍後再試。",
    "login.error.networkError": "無法連線至伺服器，請檢查網路後重試。",
    "login.error.unknown": "登入失敗，請稍後再試。",
  },
  en: {
    "meta.loginTitle": "Sign in · Kiln",
    "shared.skipToContent": "Skip to main content",
    "login.language": "Language",
    "login.eyebrow": "KILN ADMIN",
    "login.title": "Sign in to the admin console",
    "login.description": "Manage channels, access controls, network egress, and system settings.",
    "login.username": "Username",
    "login.password": "Password",
    "login.remember": "Keep me signed in",
    "login.submit": "Sign in",
    "login.submitting": "Signing in…",
    "login.error.usernameRequired": "Enter your username.",
    "login.error.passwordRequired": "Enter your password.",
    "login.error.invalidCredentials": "The username or password is incorrect.",
    "login.error.adminRequired": "This account cannot access the admin console.",
    "login.error.rateLimited": "Too many sign-in attempts. Try again later.",
    "login.error.invalidRequest": "The sign-in details are invalid. Check them and try again.",
    "login.error.accessDenied": "The sign-in request was denied. Contact your administrator.",
    "login.error.serviceUnavailable": "The sign-in service is temporarily unavailable. Try again later.",
    "login.error.serverError": "The server could not complete sign-in. Try again later.",
    "login.error.networkError": "Unable to reach the server. Check your connection and try again.",
    "login.error.unknown": "Sign-in failed. Try again later.",
  },
};

function normalizeLocale(value) {
  const locale = String(value || "").trim().replaceAll("_", "-").toLowerCase();
  if (locale === "zh-hant" || locale.startsWith("zh-hant-") || /^zh-(tw|hk|mo)(-|$)/.test(locale)) return "zh-Hant";
  if (locale === "zh-hans" || locale.startsWith("zh-hans-") || locale.startsWith("zh-")) return "zh-Hans";
  if (locale === "zh") return "zh-Hans";
  if (locale === "en" || locale.startsWith("en-")) return "en";
  return "";
}

export function resolveLocale(savedLocale, browserLanguages = []) {
  for (const candidate of [savedLocale, ...browserLanguages]) {
    const locale = normalizeLocale(candidate);
    if (locale) return locale;
  }
  return DEFAULT_LOCALE;
}

export function createI18n(options = {}) {
  let storage = options.storage;
  if (!("storage" in options)) {
    try {
      storage = globalThis.localStorage;
    } catch {
      storage = null;
    }
  }
  let languages = options.languages;
  if (!("languages" in options)) {
    try {
      languages = globalThis.navigator?.languages ?? [];
    } catch {
      languages = [];
    }
  }
  let savedLocale = "";
  try {
    savedLocale = storage?.getItem(STORAGE_KEY) || "";
  } catch {
    /* storage can be unavailable in hardened browsers */
  }
  let locale = resolveLocale(savedLocale, languages);

  return {
    get locale() {
      return locale;
    },
    setLocale(nextLocale) {
      locale = normalizeLocale(nextLocale) || DEFAULT_LOCALE;
      try {
        storage?.setItem(STORAGE_KEY, locale);
      } catch {
        /* the selected locale still applies to this page */
      }
      return locale;
    },
    t(key) {
      return messages[locale]?.[key] || messages[DEFAULT_LOCALE]?.[key] || key;
    },
  };
}
