import { readFileSync } from "node:fs";
import { expect, type Page, type Route, test } from "@playwright/test";
import { installPreferencesApi } from "./fixtures/preferences";

// When the browser cannot decode a file's video track at all (for example HEVC without
// hardware support), the element can keep playing audio against a black picture with no
// error. The viewer must say so honestly instead of showing a silent black screen.
// Fixture: hevc-unsupported.mkv = synthetic test pattern (HEVC video) + 440 Hz tone (AAC),
// generated with libx265 - see fixtures/player/generate.sh.

const now = "2026-10-31T12:00:00Z";
const token = "tds_codecNotice123";
const folderId = "a1111111-1111-4111-8111-111111111111";

type Item = { id: string; name: string; mimeType: string; data: Buffer };
const hevc: Item = {
  id: "b0000000-0000-4000-8000-0000000000e2",
  name: "Hevc.mkv",
  mimeType: "video/x-matroska",
  data: readFileSync(new URL("./fixtures/player/hevc-unsupported.mkv", import.meta.url)),
};
const h264: Item = {
  id: "b0000000-0000-4000-8000-0000000000e3",
  name: "Plain.mp4",
  mimeType: "video/mp4",
  data: readFileSync(new URL("./fixtures/player/multi-track.mp4", import.meta.url)),
};

async function installShare(page: Page, items: Item[]) {
  const prefix = `/api/v1/public/shares/${token}`;
  await page.route(
    (url) => url.pathname.startsWith("/api/"),
    async (route: Route) => {
      const url = new URL(route.request().url());
      if (url.pathname === prefix) {
        return route.fulfill({
          json: {
            id: "c3333333-3333-4333-8333-333333333333",
            file: {
              id: folderId, name: "Videos", kind: "folder", mimeType: "drive/folder",
              encryption: false, status: "active", modTime: now, generation: 1,
              createdAt: now, updatedAt: now,
            },
            passwordProtected: false,
            permission: "read",
            embeddedTracks: true,
          },
        });
      }
      if (url.pathname === `${prefix}/files`) {
        return route.fulfill({
          json: {
            items: items.map((i) => ({
              id: i.id, parentId: folderId, name: i.name, kind: "file", mimeType: i.mimeType,
              size: i.data.length, encryption: false, status: "active", modTime: now,
              generation: 1, createdAt: now, updatedAt: now,
            })),
          },
        });
      }
      for (const item of items) {
        if (url.pathname.startsWith(`${prefix}/files/${item.id}/content/`)) {
          const header = route.request().headers().range ?? "";
          const range = /^bytes=(\d+)-(\d*)$/.exec(header);
          const data = item.data;
          if (!range) {
            return route.fulfill({ status: 200, body: data, headers: { "Accept-Ranges": "bytes" } });
          }
          const start = Number(range[1]);
          const end = range[2] ? Math.min(Number(range[2]), data.length - 1) : data.length - 1;
          if (start >= data.length) return route.fulfill({ status: 416 });
          return route.fulfill({
            status: 206,
            body: data.subarray(start, end + 1),
            headers: { "Accept-Ranges": "bytes", "Content-Range": `bytes ${start}-${end}/${data.length}` },
          });
        }
      }
      return route.fulfill({ status: 404, json: { code: "not_found", title: "not found" } });
    },
  );
  await installPreferencesApi(page);
}

const notice = (page: Page) => page.locator("[data-codec-notice]");
const NOTICE_TEXT = "can't decode the picture";
const video = (page: Page) => page.locator("[data-content-viewer] video");

async function openVideo(page: Page, name: string) {
  await page.goto(`/share/${token}`);
  await page.getByTitle(name).dblclick();
  await expect(video(page)).toBeVisible({ timeout: 20_000 });
}

test.describe("unsupported video codec notice", () => {
  test.setTimeout(90_000);
  test.skip(({ isMobile }) => isMobile, "desktop menus only");

  test("warns when the browser decodes zero video frames, and playback is not killed", async ({
    page,
  }) => {
    await installShare(page, [hevc]);
    await openVideo(page, hevc.name);
    const decodesHevc = await page.evaluate(() => {
      const v = document.createElement("video");
      return (
        v.canPlayType('video/mp4; codecs="hvc1.1.6.L120.90"') !== "" ||
        (typeof MediaSource !== "undefined" &&
          MediaSource.isTypeSupported('video/mp4; codecs="hvc1.1.6.L120.90"'))
      );
    });
    // The clock must advance either way: the notice never stops playback.
    await expect
      .poll(() => video(page).evaluate((v: HTMLVideoElement) => v.currentTime))
      .toBeGreaterThan(0.3);
    if (decodesHevc) {
      // A browser that decodes HEVC shows the picture and never the notice.
      await page.waitForTimeout(6_000);
      await expect(notice(page)).toHaveCount(0);
      return;
    }
    await expect(notice(page)).toBeVisible({ timeout: 15_000 });
    await expect(notice(page)).toContainText(NOTICE_TEXT);
    // Still playing behind the notice.
    await expect
      .poll(() => video(page).evaluate((v: HTMLVideoElement) => v.currentTime))
      .toBeGreaterThan(1);
    // Dismissible, and it stays dismissed.
    await notice(page).getByRole("button", { name: "Dismiss" }).click({ force: true });
    await expect(notice(page)).toHaveCount(0);
    await page.waitForTimeout(2_000);
    await expect(notice(page)).toHaveCount(0);
  });

  test("stays quiet for a codec the browser decodes", async ({ page }) => {
    await installShare(page, [h264]);
    await openVideo(page, h264.name);
    await expect
      .poll(() => video(page).evaluate((v: HTMLVideoElement) => v.currentTime))
      .toBeGreaterThan(1);
    await page.waitForTimeout(5_000);
    await expect(notice(page)).toHaveCount(0);
  });
});
