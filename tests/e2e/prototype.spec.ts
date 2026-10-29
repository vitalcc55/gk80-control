import { expect, test } from '@playwright/test';

for (const viewport of [
  { width: 1366, height: 768 },
  { width: 1920, height: 1080 },
]) {
  test.describe(`прототип ${viewport.width}×${viewport.height}`, () => {
    test.use({ viewport });

    test('оператор проходит пять экранов и видит настоящий график', async ({ page }) => {
      const errors: string[] = [];
      page.on('pageerror', (error) => errors.push(error.message));
      await page.goto('/?fixture=empty');
      await expect(page.getByText('Макет · подготовленные данные')).toBeVisible();
      await page.getByRole('button', { name: 'Новое испытание' }).first().click();
      await expect(page.getByRole('heading', { name: 'Параметры испытания' })).toBeVisible();
      await expect(
        page.getByRole('textbox', { name: 'Площадь клапана, квадратные метры' }),
      ).toBeVisible();
      await expect(page.locator('label:has(#parameter-area) sup')).toHaveText('2');
      await expect(page.locator('label:has(#parameter-pmax) sub')).toHaveText('max');
      await page.evaluate(async () => {
        await document.fonts.ready;
      });
      const typography = await page.locator('label:has(#parameter-pmax) sub').evaluate((index) => ({
        font: getComputedStyle(index).fontFamily,
        loaded: document.fonts.check('14px "Medeo Sans"', 'Pmax'),
        faceLoaded: [...document.fonts].some(
          (face) => face.family === 'Medeo Sans' && face.status === 'loaded',
        ),
        lowered: Number.parseFloat(getComputedStyle(index).bottom) < 0,
      }));
      expect(typography.font).toContain('Medeo Sans');
      expect(typography.loaded).toBe(true);
      expect(typography.faceLoaded).toBe(true);
      expect(typography.lowered).toBe(true);
      const raised = await page
        .locator('label:has(#parameter-area) sup')
        .evaluate((index) => Number.parseFloat(getComputedStyle(index).top) < 0);
      expect(raised).toBe(true);
      await page
        .getByRole('textbox', { name: 'Обозначение или наименование клапана' })
        .fill('Клапан для показа');
      await page.getByRole('button', { name: 'Проверить готовность' }).click();
      await expect(page.getByRole('heading', { name: 'Готовность', exact: true })).toBeVisible();
      const start = page.getByRole('button', { name: 'Перейти к проведению' });
      await expect(start).toBeDisabled();
      await page.getByRole('checkbox', { name: /Клапан закрыт/ }).check();
      await page.getByRole('checkbox', { name: /Подключение соответствует направлению/ }).check();
      await start.click();
      await expect(page.getByRole('heading', { name: 'Проведение испытания' })).toBeVisible();
      await expect(page.getByText('Клапан для показа')).toBeVisible();
      await expect(page.locator('.chart-container canvas').first()).toBeVisible();
      await expect(page.getByRole('button', { name: 'Остановить испытание' })).toBeVisible();
      const longAction = page.getByRole('button', { name: 'Подтверждаю завершение серии' });
      const labelFits = await longAction.evaluate((button) => {
        const label = button.querySelector('span');
        if (!label) return false;
        const outer = button.getBoundingClientRect();
        const inner = label.getBoundingClientRect();
        return (
          inner.left >= outer.left &&
          inner.right <= outer.right &&
          inner.top >= outer.top &&
          inner.bottom <= outer.bottom &&
          label.scrollWidth <= label.clientWidth
        );
      });
      expect(labelFits, 'Длинная подпись помещается в кнопку').toBe(true);
      await page.evaluate(() => window.scrollTo(0, 0));
      const toolbar = await page.locator('.fixture-toolbar').boundingBox();
      expect(toolbar).not.toBeNull();
      if (!toolbar) throw new Error('Fixture toolbar is not visible');
      for (const selector of [
        '.main-reading strong',
        '.target-reading strong',
        '.differential-reading strong',
        '.chart-container',
        '.action-panel h2',
        '.action-instruction',
        '.stop-button',
      ]) {
        const element = page.locator(selector);
        const box = await element.boundingBox();
        expect(box, selector).not.toBeNull();
        if (!box) throw new Error(`Element is not visible: ${selector}`);
        expect(box.x, selector).toBeGreaterThanOrEqual(0);
        expect(box.y, selector).toBeGreaterThanOrEqual(0);
        expect(box.x + box.width, selector).toBeLessThanOrEqual(viewport.width);
        expect(box.y + box.height, selector).toBeLessThanOrEqual(viewport.height);
        const intersectsToolbar =
          box.x < toolbar.x + toolbar.width &&
          box.x + box.width > toolbar.x &&
          box.y < toolbar.y + toolbar.height &&
          box.y + box.height > toolbar.y;
        expect(intersectsToolbar, `Element overlaps fixture toolbar: ${selector}`).toBe(false);
        const visibleAtCenter = await element.evaluate((node) => {
          const bounds = node.getBoundingClientRect();
          const hit = document.elementFromPoint(
            bounds.left + bounds.width / 2,
            bounds.top + bounds.height / 2,
          );
          return hit !== null && (node === hit || node.contains(hit));
        });
        expect(visibleAtCenter, `Element obscured at center: ${selector}`).toBe(true);
      }
      for (const action of [
        'Подтверждаю завершение серии',
        'Показать стабилизацию',
        'Показать следующий этап',
        'Подтверждаю открытие',
        'Подтверждаю закрытие',
        'Показать измерение',
        'Показать снижение давления',
        'Показать результат',
      ]) {
        await page.getByRole('button', { name: action }).click();
      }
      await expect(page.getByRole('heading', { name: 'Результат испытания' })).toBeVisible();
      await expect(page.getByText('Не рассчитан')).toBeVisible();
      await expect(page.getByText('Не определён')).toBeVisible();
      expect(errors).toEqual([]);
    });

    test('состояние потери ДД1 воспроизводимо по прямой ссылке', async ({ page }) => {
      await page.goto('/?fixture=sensor-lost&step=7');
      await expect(page.getByRole('heading', { name: 'Проведение испытания' })).toBeVisible();
      await expect(page.locator('.main-reading strong')).toHaveText('Неизвестно');
      await expect(page.locator('.main-reading')).toContainText('устарело');
      await expect(
        page.getByText('Данные ДД1 недоступны. Текущее давление неизвестно.'),
      ).toBeVisible();
      await page.reload();
      await expect(page.locator('.main-reading strong')).toHaveText('Неизвестно');
      await page.getByRole('button', { name: 'Остановить испытание' }).click();
      await expect(page).toHaveURL(/sensor=lost/);
      await expect(page.locator('.main-reading strong')).toHaveText('Неизвестно');
      await page.reload();
      await expect(page.getByRole('button', { name: 'Остановка выполняется' })).toBeDisabled();
      await expect(page.locator('.main-reading strong')).toHaveText('Неизвестно');
      await expect(page.locator('.main-reading')).toContainText('устарело');
    });

    test('остановка сохраняет подготовленные показания по умолчанию после обновления', async ({
      page,
    }) => {
      await page.goto('/?fixture=actuator');
      await page.getByRole('button', { name: 'Остановить испытание' }).click();
      await expect(page.locator('.main-reading strong')).toHaveText('Неизвестно');
      await expect(page.locator('.differential-reading strong')).toHaveText('Неизвестно');
      await page.reload();
      await expect(page.locator('.main-reading strong')).toHaveText('Неизвестно');
      await expect(page.locator('.differential-reading strong')).toHaveText('Неизвестно');
      await page.goto('/?fixture=waiting&step=7');
      await expect(page.locator('.main-reading strong')).toContainText('2 000');
      await page.getByRole('button', { name: 'Остановить испытание' }).click();
      await expect(page.locator('.main-reading strong')).toContainText('2 000');
      await page.reload();
      await expect(page.locator('.main-reading strong')).toContainText('2 000');
      await expect(page.getByRole('button', { name: 'Остановка выполняется' })).toBeDisabled();
    });

    test('навигация сохраняет черновик и возвращает к активному проходу', async ({ page }) => {
      await page.goto('/?fixture=empty');
      await page.getByRole('button', { name: 'Новое испытание' }).click();
      await page
        .getByRole('textbox', { name: 'Обозначение или наименование клапана' })
        .fill('Клапан навигации');
      await page.getByRole('textbox', { name: 'Площадь клапана, квадратные метры' }).fill('0,');
      const nav = page.getByRole('navigation', { name: 'Навигация' });
      await nav.getByRole('button', { name: 'Испытания' }).click();
      await nav.getByRole('button', { name: 'Параметры' }).click();
      await expect(
        page.getByRole('textbox', { name: 'Площадь клапана, квадратные метры' }),
      ).toHaveValue('0,');
      await page.getByRole('textbox', { name: 'Площадь клапана, квадратные метры' }).fill('0,12');
      await page.getByRole('button', { name: 'Проверить готовность' }).click();
      await page.getByRole('checkbox', { name: /Клапан закрыт/ }).check();
      await page.getByRole('checkbox', { name: /Подключение соответствует направлению/ }).check();
      await page.getByRole('button', { name: 'Перейти к проведению' }).click();
      await page.getByRole('button', { name: 'Подтверждаю завершение серии' }).click();
      await nav.getByRole('button', { name: 'Параметры' }).click();
      await expect(page.getByText('Текущее испытание · только просмотр')).toBeVisible();
      await expect(page.getByText('Клапан навигации')).toBeVisible();
      await expect(page.getByRole('textbox')).toHaveCount(0);
      await nav.getByRole('button', { name: 'Текущее испытание' }).click();
      await expect(page.getByRole('heading', { name: 'Выход на рабочее давление' })).toBeVisible();
      await expect(page.getByRole('button', { name: 'Остановить испытание' })).toBeVisible();
    });
  });
}
