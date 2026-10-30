import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import '../index.css';
import './desktop.css';
import { DesktopApp } from './DesktopApp';
import { desktopBridge, desktopDemoService, sessionBridge } from './wails-bridge';

const root = document.getElementById('root');
if (!root) throw new Error('Root element is missing');
createRoot(root).render(
  <StrictMode>
    <DesktopApp bridge={desktopBridge} session={sessionBridge} service={desktopDemoService} />
  </StrictMode>,
);
