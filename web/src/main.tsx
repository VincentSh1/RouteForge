import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { App } from './App';
import { Auth } from './Auth';
import './style.css';

createRoot(document.getElementById('root')!).render(<StrictMode><Auth><App /></Auth></StrictMode>);
