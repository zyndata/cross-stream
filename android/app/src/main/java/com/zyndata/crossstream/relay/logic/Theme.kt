package com.zyndata.crossstream.relay.logic

/** App color theme (Settings → Theme); SYSTEM follows the phone's dark mode. */
enum class ThemeMode(val key: String) {
    SYSTEM("system"),
    LIGHT("light"),
    DARK("dark"),
    ;

    /** Whether the dark color scheme is used, given the phone's dark mode. */
    fun dark(systemDark: Boolean): Boolean = when (this) {
        SYSTEM -> systemDark
        LIGHT -> false
        DARK -> true
    }

    companion object {
        fun of(s: String?): ThemeMode = entries.firstOrNull { it.key == s } ?: SYSTEM
    }
}
