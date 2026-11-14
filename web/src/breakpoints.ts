// Viewport breakpoint shared by the layout and the stylesheet.
//
// The sidebar is a fixed-width rail, so on a phone it has to collapse to its
// icon-only width: a 208px aside on a 390px viewport leaves 182px for the page,
// which is narrower than a single table column. styles.css restates the same
// number in its media queries, and web/tests/styles.test.ts asserts the two
// agree so the layout cannot end up switching at a different width than the CSS.

/** Width at or below which the panel switches to its compact layout. */
export const MOBILE_BREAKPOINT = 782
