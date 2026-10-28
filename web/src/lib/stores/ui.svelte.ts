export const ui = $state<{
  newTask: boolean
  newTaskProject: string
  addProject: boolean
  drawer: boolean
  sendBack: { pid: string; id: string } | null
}>({ newTask: false, newTaskProject: '', addProject: false, drawer: false, sendBack: null })

export function openNewTask(projectId = '') {
  ui.newTaskProject = projectId
  ui.newTask = true
}
