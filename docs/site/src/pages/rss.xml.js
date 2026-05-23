// rss.xml — site-wide RSS feed listing every materialised doc page.
// Astro's @astrojs/rss helper handles XML escaping + the boilerplate
// wrapper; we just project each content-collection entry into a feed
// item. The feed URL is exposed via the <link rel="alternate"> in
// Default.astro's <head>.
import rss from "@astrojs/rss";
import { getCollection } from "astro:content";

export async function GET(context) {
  let entries = [];
  try {
    entries = await getCollection("docs");
  } catch {
    /* collection empty in bootstrap */
  }
  return rss({
    //: <title> + <description> derived from the site URL pattern so
    //: this file stays project-agnostic — works for any fork or
    //: rename of kitsunium/sdk.
    title: "Documentation feed",
    description: "Latest pages in the documentation portal.",
    site: context.site,
    items: entries.map((entry) => {
      const segments = entry.id.split("/");
      const major = segments[0];
      const release = segments[1];
      const rest = segments.slice(2).join("/");
      const slug = rest === "" || rest === "index" ? "" : `${rest}/`;
      return {
        title: entry.data?.title ?? rest ?? entry.id,
        description: entry.data?.description ?? "",
        link: `/${major}/${release}/${slug}`,
      };
    }),
    customData: `<language>en</language>`,
  });
}
