import { createRoot } from 'react-dom/client';
import '../index.css';
import '../desktop/desktop.css';
import { BrowserApp } from './BrowserApp';

const root = document.getElementById('root');
if (!root) throw new Error('Root element is missing');
createRoot(root).render(<BrowserApp />);
