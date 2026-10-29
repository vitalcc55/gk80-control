import { expect, test } from '@playwright/test';

test('маршрут пяти экранов доступен с клавиатуры', async ({ page }) => {
  await page.goto('/?fixture=empty');
  await expect(page.getByRole('main', { name: 'Испытания' })).toBeFocused();
  await page.keyboard.press('Tab');
  await expect(page.getByRole('button', { name: 'Новое испытание' })).toBeFocused();
  await page.keyboard.press('Enter');
  await expect(page.getByRole('main', { name: 'Параметры' })).toBeFocused();
  await page.keyboard.press('Tab');
  await expect(page.locator('#parameter-valve')).toBeFocused();
  for (let index = 0; index < 7; index++) await page.keyboard.press('Tab');
  await page.keyboard.press('Enter');
  await expect(page.locator('#parameter-valve')).toBeFocused();
  await expect(page.locator('#parameter-valve')).toHaveAttribute('aria-invalid', 'true');
  await page.keyboard.type('Клапан');
  for (let index = 0; index < 7; index++) await page.keyboard.press('Tab');
  await expect(page.getByRole('button', { name: 'Проверить готовность' })).toBeFocused();
  await page.keyboard.press('Enter');
  await expect(page.getByRole('main', { name: 'Готовность' })).toBeFocused();
  await page.keyboard.press('Tab');
  await page.keyboard.press('Space');
  await page.keyboard.press('Tab');
  await page.keyboard.press('Space');
  await page.keyboard.press('Tab');
  await page.keyboard.press('Tab');
  await expect(page.getByRole('button', { name: 'Перейти к проведению' })).toBeFocused();
  await page.keyboard.press('Enter');
  await expect(page.getByRole('main', { name: 'Проведение' })).toBeFocused();
  await page.keyboard.press('Tab');
  await page.keyboard.press('Tab');
  await expect(page.locator('.chart-data summary')).toBeFocused();
  await page.keyboard.press('Enter');
  await expect(page.locator('.chart-data')).toHaveAttribute('open', '');
  await page.keyboard.press('Enter');
  await page.keyboard.press('Tab');
  const advance = page.locator('.action-panel .primary-button');
  await expect(advance).toBeFocused();
  for (let index = 0; index < 8; index++) await page.keyboard.press('Enter');
  await expect(page.getByRole('main', { name: 'Результат' })).toBeFocused();
  await page.keyboard.press('Tab');
  await expect(page.getByRole('button', { name: 'К списку' })).toBeFocused();
  await page.keyboard.press('Enter');
  await expect(page.getByRole('main', { name: 'Испытания' })).toBeFocused();
  await expect(page.getByText('Клапан').first()).toBeVisible();
});

test('фокус остаётся у состояния после клавиатурной остановки', async ({ page }) => {
  await page.goto('/?fixture=measuring');
  await expect(page.getByRole('main', { name: 'Проведение' })).toBeFocused();
  for (let index = 0; index < 4; index++) await page.keyboard.press('Tab');
  await expect(page.getByRole('button', { name: 'Остановить испытание' })).toBeFocused();
  await page.keyboard.press('Enter');
  const status = page.getByRole('heading', { name: 'Остановка', exact: true });
  await expect(status).toBeFocused();
  expect(await status.evaluate((node) => node.matches(':focus-visible'))).toBe(true);
  expect(await status.evaluate((node) => getComputedStyle(node).outlineStyle)).not.toBe('none');
  await expect(page.getByRole('button', { name: 'Остановка выполняется' })).toBeDisabled();
  await page.keyboard.press('Tab');
  await expect(
    page.getByRole('combobox', { name: 'Выбрать подготовленное состояние' }),
  ).toBeFocused();
});

test('стрелки переключают направление и привод в параметрах', async ({ page }) => {
  await page.goto('/?fixture=empty');
  await expect(page.getByRole('main', { name: 'Испытания' })).toBeFocused();
  await page.keyboard.press('Tab');
  await page.keyboard.press('Enter');
  await expect(page.getByRole('main', { name: 'Параметры' })).toBeFocused();
  for (let index = 0; index < 5; index++) await page.keyboard.press('Tab');
  await expect(page.getByRole('radio', { name: 'Нагнетание' })).toBeFocused();
  await page.keyboard.press('ArrowRight');
  await expect(page.getByRole('radio', { name: 'Разрежение' })).toBeChecked();
  await expect(page.getByText(/Диафрагма №\s*2/)).toBeVisible();
  await page.keyboard.press('Tab');
  await expect(page.getByRole('radio', { name: 'Нет', exact: true })).toBeFocused();
  await page.keyboard.press('ArrowRight');
  await expect(page.getByRole('radio', { name: 'Есть', exact: true })).toBeChecked();
  await expect(page.locator('#parameter-openSeconds')).toBeVisible();
  await expect(page.locator('#parameter-closeSeconds')).toBeVisible();
});

test('отказ готовности не разблокируется подтверждениями с клавиатуры', async ({ page }) => {
  await page.goto('/?fixture=readiness-failed');
  await expect(page.getByRole('main', { name: 'Готовность' })).toBeFocused();
  await page.keyboard.press('Tab');
  await page.keyboard.press('Space');
  await page.keyboard.press('Tab');
  await page.keyboard.press('Space');
  await expect(page.getByRole('button', { name: 'Перейти к проведению' })).toBeDisabled();
  await page.keyboard.press('Tab');
  await expect(page.getByRole('button', { name: 'К параметрам' })).toBeFocused();
  await page.keyboard.press('Tab');
  await expect(
    page.getByRole('combobox', { name: 'Выбрать подготовленное состояние' }),
  ).toBeFocused();
});

for (const viewport of [
  // CSS-эквивалент 1366×768 при 150%; не подтверждает штатный масштаб браузера.
  { width: 910, height: 512 },
  { width: 1024, height: 768 },
  { width: 1280, height: 720 },
  { width: 1366, height: 768 },
  { width: 1920, height: 1080 },
]) {
  test.describe(`доступность рабочего экрана ${viewport.width}×${viewport.height}`, () => {
    test.use({ viewport });

    for (const fixture of ['measuring', 'sensor-lost', 'actuator']) {
      test(`${fixture}: показания и остановка доступны при раскрытой таблице`, async ({ page }) => {
        await page.goto(`/?fixture=${fixture}`);
        await page.evaluate(async () => {
          await document.fonts.ready;
        });

        // Проверяем именно границы текста, а не ширину ячейки: значения могут выходить за неё.
        const readingsIntersect = await page.evaluate(() => {
          const selectors = [
            '.main-reading strong',
            '.target-reading strong',
            '.differential-reading strong',
          ];
          const rectangles = selectors.map((selector) => {
            const node = document.querySelector(selector);
            if (!node) throw new Error(`Отсутствует показание: ${selector}`);
            const range = document.createRange();
            range.selectNodeContents(node);
            const rect = range.getBoundingClientRect();
            return { left: rect.left, right: rect.right, top: rect.top, bottom: rect.bottom };
          });
          return rectangles.some((a, index) =>
            rectangles
              .slice(index + 1)
              .some(
                (b) => a.left < b.right && a.right > b.left && a.top < b.bottom && a.bottom > b.top,
              ),
          );
        });
        expect(readingsIntersect, 'Текст показаний не должен накладываться').toBe(false);

        const summary = page.locator('.chart-data summary');
        await summary.focus();
        await page.keyboard.press('Enter');
        await expect(page.locator('.chart-data')).toHaveAttribute('open', '');
        await expect(
          page.getByRole('table', { name: 'Подготовленные показания по времени' }),
        ).toBeVisible();

        const documentWidth = await page.evaluate(() => ({
          available: document.documentElement.clientWidth,
          content: Math.max(document.documentElement.scrollWidth, document.body.scrollWidth),
        }));
        expect(
          documentWidth.content,
          'Документ не должен требовать горизонтальной прокрутки',
        ).toBeLessThanOrEqual(documentWidth.available);

        const stop = page.getByRole('button', { name: 'Остановить испытание' });
        await expect(stop).toBeVisible();
        const geometry = await page.evaluate(() => {
          const button = document.querySelector('.stop-button');
          const toolbar = document.querySelector('.fixture-toolbar');
          if (!button || !toolbar)
            throw new Error('Отсутствует кнопка остановки или нижняя панель');
          const bounds = button.getBoundingClientRect();
          const footer = toolbar.getBoundingClientRect();
          const center = document.elementFromPoint(
            bounds.left + bounds.width / 2,
            bounds.top + bounds.height / 2,
          );
          return {
            left: bounds.left,
            right: bounds.right,
            top: bounds.top,
            bottom: bounds.bottom,
            footerTop: footer.top,
            centerVisible: center !== null && (center === button || button.contains(center)),
          };
        });
        expect(geometry.left).toBeGreaterThanOrEqual(0);
        expect(geometry.right).toBeLessThanOrEqual(viewport.width);
        expect(geometry.top).toBeGreaterThanOrEqual(0);
        expect(geometry.bottom).toBeLessThanOrEqual(geometry.footerTop);
        expect(geometry.centerVisible, 'Кнопка не должна быть перекрыта').toBe(true);
      });
    }
  });
}

test('ошибки полей описаны отдельно и не меняют их доступные имена', async ({ page }) => {
  await page.goto('/?fixture=empty');
  await page.getByRole('button', { name: 'Новое испытание' }).click();
  const valve = page.locator('#parameter-valve');
  const pmax = page.locator('#parameter-pmax');
  const period = page.locator('#parameter-measurementSeconds');
  await pmax.fill('0');
  await period.fill('0');
  await page.getByRole('button', { name: 'Проверить готовность' }).click();
  await expect(valve).toBeFocused();
  await expect(valve).toHaveAccessibleName('Обозначение или наименование клапана');
  await expect(valve).toHaveAccessibleDescription('Укажите обозначение или наименование клапана.');
  await expect(pmax).toHaveAccessibleName('Pmax производителя, Па');
  await expect(pmax).toHaveAccessibleDescription('Введите максимальное давление больше нуля, Па.');
  await expect(period).toHaveAccessibleName('Период усреднения утечки, с');
  await expect(period).toHaveAccessibleDescription('Введите длительность больше нуля, с.');
});

test('клавиатурный фокус селектора различим на тёмной панели', async ({ page }) => {
  await page.goto('/?fixture=measuring');
  const select = page.getByRole('combobox', { name: 'Выбрать подготовленное состояние' });
  // Сначала используем клавиатуру, чтобы браузер включил :focus-visible.
  await page.keyboard.press('Tab');
  await select.focus();
  await expect(select).toBeFocused();
  const result = await select.evaluate((node) => {
    const styles = getComputedStyle(node);
    const footer = node.closest('.fixture-toolbar');
    if (!footer) throw new Error('Нижняя панель отсутствует');
    const rgb = (color: string) => {
      const channels = color
        .match(/\d+(?:\.\d+)?/g)
        ?.slice(0, 3)
        .map(Number);
      if (channels?.length !== 3) throw new Error(`Не удалось разобрать цвет ${color}`);
      return channels;
    };
    const luminance = (color: string) => {
      const [r = 0, g = 0, b = 0] = rgb(color).map((channel) => {
        const normalized = channel / 255;
        return normalized <= 0.04045 ? normalized / 12.92 : ((normalized + 0.055) / 1.055) ** 2.4;
      });
      return 0.2126 * r + 0.7152 * g + 0.0722 * b;
    };
    const a = luminance(styles.outlineColor);
    const b = luminance(getComputedStyle(footer).backgroundColor);
    return {
      style: styles.outlineStyle,
      width: Number.parseFloat(styles.outlineWidth),
      contrast: (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05),
    };
  });
  expect(result.style).not.toBe('none');
  expect(result.width).toBeGreaterThan(0);
  expect(result.contrast).toBeGreaterThanOrEqual(3);
});
