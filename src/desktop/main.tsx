import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import '../index.css';
import './desktop.css';
import { desktopBridge } from './bridge';
import { DesktopApp } from './DesktopApp';

const root = document.getElementById('root');
if (!root) throw new Error('Root element is missing');
createRoot(root).render(
  <StrictMode>
    <DesktopApp bridge={desktopBridge} />
  </StrictMode>,
);
