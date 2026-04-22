import React, { useState, useEffect } from 'react';

const API_BASE = "/api";

function App() {
  const [messages, setMessages] = useState([]);
  const [input, setInput] = useState("");
  const [chatId, setChatId] = useState(null);
  const [nextKey, setNextKey] = useState(null);

  // Initialize Chat
  const startNewChat = async () => {
    try {
      const res = await fetch(`${API_BASE}/init_chat`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ llm: "chatgpt", user_id: "sss@ff.com", adapter_id: "ada1" })
      });
      const data = await res.json();
      if (data.status === "success") {
        setChatId(data.chat_id);
        setNextKey(data.set_next_req_key);
        setMessages([{ role: 'system', text: "New chat started." }]);
      }
    } catch (err) { console.error("Failed to init chat", err); }
  };

  const sendMessage = async (e) => {
    e.preventDefault();
    if (!input || !chatId) return;

    const userText = input;
    setInput("");
    setMessages(prev => [...prev, { role: 'user', text: userText }]);

    try {
      const res = await fetch(`${API_BASE}/set_req_from_adapter_sync`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          internal_chat_id: chatId,
          request_key: nextKey,
          content: { input_text: userText }
        })
      });
      const data = await res.json();
      
      if (data.status === "success") {
        setNextKey(data.set_next_req_key);
        setMessages(prev => [...prev, { role: 'assistant', text: data.content.input_text }]);
      } else {
        setMessages(prev => [...prev, { role: 'error', text: data.message || "An error occurred" }]);
      }
    } catch (err) {
      console.error("Fetch Error Details:", err); 
      setMessages(prev => [...prev, { role: 'error', text: "Connection failed." }]);
    }
  };

  const renderText = (text) => {
    return text.split('\n').map((line, i) => (
      <span key={i}>
        {line.split(/(\*\*.*?\*\*)/g).map((part, j) => 
          part.startsWith('**') && part.endsWith('**') 
            ? <strong key={j}>{part.slice(2, -2)}</strong> 
            : part
        )}
        <br />
      </span>
    ));
  };

  return (
    <div style={{ display: 'flex', height: '100vh', fontFamily: 'sans-serif' }}>
      <div style={{ width: '250px', background: '#202123', color: 'white', padding: '15px' }}>
        <button onClick={startNewChat} style={{ width: '100%', padding: '10px', cursor: 'pointer' }}>+ New Chat</button>
        {chatId && <div style={{ marginTop: '20px', fontSize: '12px' }}>ID: {chatId.slice(0,8)}...</div>}
        <button onClick={() => {setChatId(null); setMessages([]);}} style={{ marginTop: 'auto', bottom: '20px', position: 'absolute' }}>Clear Session</button>
      </div>
      <div style={{ flex: 1, display: 'flex', flexDirection: 'column', background: '#343541', color: 'white' }}>
        <div style={{ flex: 1, overflowY: 'auto', padding: '20px' }}>
          {messages.map((m, i) => (
            <div key={i} style={{ marginBottom: '20px', textAlign: m.role === 'user' ? 'right' : 'left' }}>
              <div style={{ display: 'inline-block', padding: '10px', borderRadius: '8px', background: m.role === 'user' ? '#444654' : 'transparent', maxWidth: '80%' }}>
                {renderText(m.text)}
              </div>
            </div>
          ))}
        </div>
        <form onSubmit={sendMessage} style={{ padding: '20px', borderTop: '1px solid #4d4d4f' }}>
          <input 
            value={input} 
            onChange={(e) => setInput(e.target.value)}
            placeholder="Type a message..."
            style={{ width: '100%', padding: '12px', borderRadius: '5px', border: 'none', background: '#40414f', color: 'white' }}
          />
        </form>
      </div>
    </div>
  );
}

export default App;