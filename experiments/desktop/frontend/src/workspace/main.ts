import { mount } from 'svelte';
import Workspace from './Workspace.svelte';
import './workspace.css';

mount(Workspace, { target: document.getElementById('app')! });
