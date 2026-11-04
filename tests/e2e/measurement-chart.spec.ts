import { expect, test } from '@playwright/test';

test('заданный интервал переживает double-click, а курсор обновляется при разрыве без смены шкал', async ({
  page,
}) => {
  await page.addInitScript(() => {
    const text = CanvasRenderingContext2D.prototype.fillText;
    const clear = CanvasRenderingContext2D.prototype.clearRect;
    CanvasRenderingContext2D.prototype.clearRect = function (x, y, width, height) {
      this.canvas.dataset['axisText'] = '';
      clear.call(this, x, y, width, height);
    };
    CanvasRenderingContext2D.prototype.fillText = function (value, x, y, maxWidth) {
      this.canvas.dataset['axisText'] = `${this.canvas.dataset['axisText'] ?? ''}\u001f${value}`;
      if (maxWidth === undefined) text.call(this, value, x, y);
      else text.call(this, value, x, y, maxWidth);
    };
  });
  await page.goto('/tests/e2e/chart.html');
  const canvas = page.locator('.chart-container canvas').first();
  await expect(canvas).toHaveAttribute('data-axis-text', /Перепад, Па/);
  const xLabels = () =>
    canvas.evaluate((element) => {
      const labels = (element.dataset['axisText'] ?? '').split('\u001f');
      const start = labels.lastIndexOf('Время, с');
      const end = labels.indexOf('Давление, Па', start);
      return labels.slice(start + 1, end);
    });
  expect(await xLabels()).toContain('60');
  const overlay = page.locator('.chart-container .u-over');
  await overlay.dblclick();
  await page.evaluate(() => new Promise(requestAnimationFrame));
  expect(await xLabels()).toContain('60');

  await page.getByRole('button', { name: 'Разрыв', exact: true }).focus();
  const box = await overlay.boundingBox();
  if (!box) throw new Error('Plot overlay is missing');
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
  const marker = page.locator('.chart-container .u-cursor-pt').first();
  await expect(marker).toBeVisible();
  const before = await marker.getAttribute('style');
  await page.keyboard.press('Enter');
  await expect(page.locator('.chart-data-table tbody tr').last()).toContainText('Нет данных');
  await expect.poll(() => marker.getAttribute('style')).not.toBe(before);
  await expect(marker).toBeHidden();
  await page.getByRole('button', { name: 'Авто', exact: true }).click();
  await expect.poll(xLabels).toContain('30');
  expect(await xLabels()).not.toContain('60');
});
